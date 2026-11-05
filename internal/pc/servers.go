package pc

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/servers"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// Server management (api/desktop, ADR 0004 milestone 4). The logic lives in
// internal/pc/servers; this file wires it to the lifecycle: the tunnel secret
// file, the cached tunnel outbound and the reload path.

// Server is the non-secret summary of a stored tunnel server.
type Server = servers.Server

// The error kinds of the server calls. Test them with errors.Is. The errors
// of invalid_input and unsupported_protocol carry a fixed sentence (a
// UserMessage method) that names the kind of problem and never a value.
var (
	ErrInvalidInput        = servers.ErrInvalidInput
	ErrUnsupportedProtocol = servers.ErrUnsupportedProtocol
	ErrDuplicate           = servers.ErrDuplicate
	ErrLimit               = servers.ErrLimit
	ErrServerNotFound      = servers.ErrNotFound
	ErrInputTooLarge       = servers.ErrTooLarge
	// ErrBusy: a connect or disconnect is in progress, or the active server is deleted while connected.
	ErrBusy = errors.New("pc: busy")
)

// serverStore opens the store on first use (New touches no file).
func (a *App) serverStore() (*servers.Store, error) {
	a.storeMu.Lock()
	defer a.storeMu.Unlock()
	if a.store == nil {
		st, err := servers.New(filepath.Join(a.p.Secrets, "servers"), servers.OSFS(), a.checkOutbound, nil)
		if err != nil {
			return nil, err
		}
		a.store = st
	}
	return a.store, nil
}

func (a *App) tunnelPath() string { return filepath.Join(a.p.Secrets, a.cfg.Secrets.Tunnel) }

// checkOutbound is the dry run of a candidate tunnel outbound: it renders the
// full sing-box config with it and runs `sing-box check`. Without a sing-box
// binary only the rendering is checked.
func (a *App) checkOutbound(ctx context.Context, outbound json.RawMessage) error {
	bins, berr := a.resolveBinaries()
	scfg := a.cfg.SingBox
	scfg.Binary = bins.singBox
	scfg.ClashSecret = "server-check"
	table, err := route.NewTable("server-check", time.Time{}, []route.Entry{
		{Domain: "tunnel.check.invalid", Route: route.Tunnel, Source: route.SourceStatic},
	})
	if err != nil {
		return err
	}
	data, err := singbox.Generate(scfg, singbox.Outbounds{Tunnel: outbound}, table)
	if err != nil {
		a.log.Debug("server dry run: config rendering failed", "err", err)
		return err
	}
	if berr != nil {
		a.log.Debug("server dry run: sing-box not found, only the rendering was checked")
		return nil
	}
	if err := os.MkdirAll(a.p.Run, 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(a.p.Run, "servercheck-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, singBoxFileName)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	if err := a.d.singCheck(ctx, bins.singBox, file); err != nil {
		a.log.Debug("server dry run: sing-box rejected the config", "err", err)
		return err
	}
	return nil
}

// Servers lists the stored servers, oldest first; never nil. Reads do not wait
// for a connect.
func (a *App) Servers() ([]Server, error) {
	st, err := a.serverStore()
	if err != nil {
		return nil, err
	}
	return st.List()
}

// AddServer parses input (a share link or one sing-box outbound JSON object),
// validates it with a dry run and stores it, not activated. name is optional.
// It is serialized with Connect, Disconnect and reload; waiting honours ctx.
func (a *App) AddServer(ctx context.Context, input, name string) (Server, error) {
	st, err := a.serverStore()
	if err != nil {
		return Server{}, err
	}
	if err := a.op.Lock(ctx); err != nil {
		return Server{}, err
	}
	defer a.op.Unlock()
	return st.Add(ctx, input, name)
}

// TestServer is the offline test of a stored server (id) or of input: parse
// plus the dry run, no connection. The protocol is returned whenever known.
func (a *App) TestServer(ctx context.Context, id, input string) (string, error) {
	st, err := a.serverStore()
	if err != nil {
		return "", err
	}
	return st.Test(ctx, id, input)
}

// activation remembers what a rollback restores.
type activation struct {
	prevActive   string
	prevFile     []byte
	hadFile      bool
	prevOutbound []byte
	prevAddrs    []netip.Addr
}

// ActivateServer makes id the tunnel server: it runs the dry run, copies the
// stored outbound over the tunnel secret file (atomically, 0600) and replaces
// the cached tunnel outbound. While connected it returns reloaded=true at once
// and the reload path applies the change (one sing-box restart, subject to the
// 30 s gap; if sing-box does not start with it, the previous server file and
// outbound are restored, the active mark stays as before and LastError is
// connect_failed). Otherwise, and when id is already active, reloaded is false.
// It is serialized with Connect, Disconnect and reload.
func (a *App) ActivateServer(ctx context.Context, id string) (reloaded bool, err error) {
	st, err := a.serverStore()
	if err != nil {
		return false, err
	}
	if _, err := st.Get(id); err != nil {
		return false, err
	}
	if a.transitioning() {
		return false, ErrBusy
	}
	if err := a.op.Lock(ctx); err != nil {
		return false, err
	}
	defer a.op.Unlock()
	if a.transitioning() {
		return false, ErrBusy
	}
	if st.Active() == id {
		return false, nil
	}
	if err := st.Verify(ctx, id); err != nil {
		return false, err
	}
	newOb, err := st.Outbound(id)
	if err != nil {
		return false, err
	}

	prev := &activation{prevActive: st.Active()}
	prev.prevFile, err = os.ReadFile(a.tunnelPath())
	prev.hadFile = err == nil
	if err := st.ExportTo(id, a.tunnelPath()); err != nil {
		return false, err
	}
	if err := st.SetActive(id); err != nil {
		a.restoreTunnelFile(prev)
		return false, err
	}
	a.mu.Lock()
	prev.prevOutbound, prev.prevAddrs = a.outbounds.Tunnel, a.tunnelAddrs
	a.outbounds.Tunnel, a.haveOutbounds = newOb, true
	a.tunnelAddrs = nil // a host-name server is looked up again at the next connect
	connected := a.state == StateConnected
	if connected && a.pending == nil {
		a.pending = prev // an earlier, still unapplied activation keeps the last confirmed server
	}
	a.mu.Unlock()
	a.log.Info("tunnel server activated", "reload", connected)
	if !connected {
		return false, nil
	}
	select {
	case a.kick <- struct{}{}:
	default:
	}
	return true, nil
}

// DeleteServer removes a server. Deleting the active one while disconnected is
// allowed: the tunnel file is removed and the next connect reports
// tunnel_missing. While connected it is ErrBusy, as is any delete during a
// connect or disconnect. Serialized with Connect, Disconnect and reload.
func (a *App) DeleteServer(ctx context.Context, id string) error {
	st, err := a.serverStore()
	if err != nil {
		return err
	}
	if _, err := st.Get(id); err != nil {
		return err
	}
	if a.transitioning() {
		return ErrBusy
	}
	if err := a.op.Lock(ctx); err != nil {
		return err
	}
	defer a.op.Unlock()
	active := st.Active() == id
	if a.transitioning() || (active && a.State() == StateConnected) {
		return ErrBusy
	}
	if err := st.Delete(id); err != nil {
		return err
	}
	if active {
		if err := os.Remove(a.tunnelPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.log.Warn("could not remove the tunnel file of the deleted server", "err", err)
		}
		a.mu.Lock()
		a.outbounds.Tunnel = nil
		a.tunnelAddrs = nil
		a.mu.Unlock()
	}
	a.log.Info("server deleted", "wasActive", active)
	return nil
}

func (a *App) transitioning() bool {
	s := a.State()
	return s == StateConnecting || s == StateDisconnecting
}

func (a *App) restoreTunnelFile(p *activation) {
	if p.hadFile {
		if err := writeAtomic(a.tunnelPath(), p.prevFile, 0o600); err != nil {
			a.log.Error("could not restore the previous tunnel file", "err", err)
		}
		return
	}
	if err := os.Remove(a.tunnelPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Error("could not remove the tunnel file", "err", err)
	}
}

// pendingActivation returns the activation whose reload has not finished.
func (a *App) pendingActivation() *activation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending
}

func (a *App) clearPending() {
	a.mu.Lock()
	a.pending = nil
	a.mu.Unlock()
}

// rollbackActivation puts the previous server back after a reload could not
// use the new one: the tunnel file, the active mark, the cached outbound. The
// failure shows as connect_failed in LastError. a.op must be held.
func (a *App) rollbackActivation(p *activation) {
	a.restoreTunnelFile(p)
	if st, err := a.serverStore(); err == nil {
		if err := st.SetActive(p.prevActive); err != nil {
			a.log.Error("could not restore the active server mark", "err", err)
		}
	}
	a.mu.Lock()
	a.outbounds.Tunnel = p.prevOutbound
	a.tunnelAddrs = p.prevAddrs
	a.pending = nil
	a.mu.Unlock()
	a.log.Warn("the new tunnel server could not be used; the previous one is back")
	msg := "The new server could not be used. The previous server is still in place."
	if len(p.prevOutbound) == 0 {
		msg = "The new server could not be used. The app keeps running without a detour server."
	}
	a.setLastError(&OpError{Code: CodeConnectFailed, Message: msg}, true)
}
