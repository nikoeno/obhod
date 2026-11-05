package desync

import (
	"bytes"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/route"
)

var update = flag.Bool("update", false, "rewrite golden files")

func presetCfg(e Engine, id string) Config {
	bin := "C:/app/winws2.exe"
	if e == NFQWS {
		bin = "/opt/zapret/nfqws2"
	}
	return Config{
		Engine:       e,
		Binary:       bin,
		Preset:       id,
		ExcludeIPs:   []netip.Prefix{netip.MustParsePrefix("203.0.113.7/32")},
		ExcludePorts: []PortRange{{50000, 50099}},
	}
}

func TestEveryPresetRenders(t *testing.T) {
	tb := desyncTable(t)
	for _, p := range Presets() {
		for _, e := range []Engine{WinWS, NFQWS} {
			t.Run(p.ID+"/"+string(e), func(t *testing.T) {
				cfg := presetCfg(e, p.ID)
				if err := cfg.Check(); err != nil {
					t.Fatalf("Check: %v", err)
				}
				plan, err := Build(cfg, uplink(), tb)
				if err != nil {
					t.Fatal(err)
				}
				checkPresetPlan(t, p, e, plan)
				again, err := Build(cfg, uplink(), tb)
				if err != nil || !slices.Equal(plan.Args, again.Args) || !slices.Equal(plan.Setup, again.Setup) {
					t.Fatalf("not deterministic: %v", err)
				}
			})
		}
	}
}

// checkPresetPlan asserts what must hold for every preset: only options the
// renderer or a profile may carry, the hostlist in every profile, the guards of
// the engine, and no blob but the catalogue's.
func checkPresetPlan(t *testing.T, p Preset, e Engine, plan Plan) {
	t.Helper()
	args := plan.Args
	owned := []string{"--wf-tcp-out=", "--wf-udp-out=", "--wf-raw-filter=", "--lua-init=@", "--qnum=", "--blob=", "--hostlist=hostlist.txt", "--uid=0:0", "--new"}
	profile := []string{"--filter-tcp=", "--filter-udp=", "--payload=", "--lua-desync="}
	hostlists, news := 0, 0
	blobNames := map[string]bool{}
	for i, a := range args {
		switch {
		case a == "--hostlist=hostlist.txt":
			hostlists++
			// every profile starts with it
			if i > 0 && args[i-1] != "--new" && !strings.HasPrefix(args[i-1], "--lua-init=@") && !strings.HasPrefix(args[i-1], "--blob=") && args[i-1] != "--uid=0:0" {
				t.Errorf("hostlist after %q", args[i-1])
			}
			if i+1 >= len(args) || !strings.HasPrefix(args[i+1], "--filter-") {
				t.Errorf("profile at %d does not start with its filter: %q", i, args[i+1:])
			}
		case a == "--new":
			news++
		case strings.HasPrefix(a, "--blob="):
			name, path, _ := strings.Cut(strings.TrimPrefix(a, "--blob="), ":@")
			file, ok := blobFiles[name]
			if !ok || !strings.HasSuffix(path, "/"+FakeDir+"/"+file) {
				t.Errorf("blob option %q is not a catalogue payload", a)
			}
			blobNames[name] = true
		default:
			if !hasAnyPrefix(a, owned) && !hasAnyPrefix(a, profile) {
				t.Errorf("unexpected argument %q", a)
			}
		}
		for _, r := range reservedOptions {
			name, _, _ := strings.Cut(a, "=")
			// the options the renderer itself sets are in owned; nothing else may match
			if (name == r || strings.HasPrefix(name, r+"-")) && !hasAnyPrefix(a, owned) {
				t.Errorf("forbidden option %q", a)
			}
		}
	}
	if hostlists != len(p.Profiles) || news != len(p.Profiles)-1 {
		t.Errorf("hostlists = %d, --new = %d for %d profiles", hostlists, news, len(p.Profiles))
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "--lua-desync=") {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(a, "--lua-desync="), ":")
		for _, f := range fields[1:] {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "blob", "seqovl_pattern", "pattern":
				if !builtinBlobs[v] && !blobNames[v] && !strings.HasPrefix(v, "0x") {
					t.Errorf("%s names blob %q that is not declared", a, v)
				}
			}
		}
	}
	// Requires lists exactly the files of the declared blobs.
	var want []string
	for n := range blobNames {
		want = append(want, fakeDirOf(planBinary(e))+"/"+blobFiles[n])
	}
	slices.Sort(want)
	if !slices.Equal(plan.Requires, want) {
		t.Errorf("Requires = %v, want %v", plan.Requires, want)
	}

	all := strings.Join(args, "\n")
	switch e {
	case WinWS:
		// uplink, tunnel exclusion and probe ports stay in the one raw filter
		for _, w := range []string{"--wf-tcp-out=80,443,2053,2083,2087,2096,8443", "--wf-udp-out=443",
			"ifIdx == 9", "(ipv6 or ip.DstAddr != 203.0.113.7)", "(not tcp or tcp.SrcPort < 50000 or tcp.SrcPort > 50099)"} {
			if !strings.Contains(all, w) {
				t.Errorf("args lack %q:\n%s", w, all)
			}
		}
		if n := strings.Count(all, "--wf-raw-filter="); n != 1 {
			t.Errorf("%d raw filters", n)
		}
	case NFQWS:
		nft := strings.Join(plan.Setup, "\n")
		for _, w := range []string{`oifname "eth0" tcp dport { 80, 443, 2053, 2083, 2087, 2096, 8443 }`, `oifname "eth0" udp dport { 443 }`,
			"ip daddr { 203.0.113.7/32 } return", "tcp sport { 50000-50099 } return", "ip rule add fwmark"} {
			if !strings.Contains(nft, w) {
				t.Errorf("setup lacks %q:\n%s", w, nft)
			}
		}
		if !slices.Contains(args, "--uid=0:0") {
			t.Error("nfqws2 would drop privileges")
		}
	}
	if plan.Files[HostlistFile] == nil {
		t.Error("no hostlist")
	}
}

func planBinary(e Engine) string { return presetCfg(e, "").Binary }

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	got = bytes.ReplaceAll(got, []byte("\\"), []byte("/")) // the same golden on every OS
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Fatalf("%s differs from golden (run with -update)\n%s", name, got)
	}
}

func TestPresetGoldenCommandLines(t *testing.T) {
	tb := desyncTable(t)
	for _, id := range []string{"alt2-multisplit", "alt3-hostfakesplit", "alt-fakedsplit", "faketls-auto"} {
		t.Run(id, func(t *testing.T) {
			plan, err := Build(presetCfg(WinWS, id), uplink(), tb)
			if err != nil {
				t.Fatal(err)
			}
			out := strings.Join(plan.Args, "\n") + "\nrequires:\n" + strings.Join(plan.Requires, "\n") + "\n"
			golden(t, "preset_"+id+"_winws.golden", []byte(out))
		})
	}
	// The Linux form shows the extra UDP queue rule next to the TCP one.
	plan, err := Build(presetCfg(NFQWS, "alt2-multisplit"), uplink(), tb)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(plan.Args, "\n") + "\nsetup:\n" + strings.Join(plan.Setup, "\n") + "\n"
	golden(t, "preset_alt2-multisplit_nfqws.golden", []byte(out))
}

func TestPresetCatalogue(t *testing.T) {
	idRe := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	seen := map[string]bool{}
	usedBlobs := map[string]bool{}
	for _, p := range Presets() {
		if !idRe.MatchString(p.ID) || p.ID == PresetCustom || p.ID == PresetAuto {
			t.Errorf("bad id %q", p.ID)
		}
		if seen[p.ID] {
			t.Errorf("duplicate id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Name == "" || p.Description == "" || !strings.HasSuffix(p.Source, ".bat") {
			t.Errorf("%s: name %q description %q source %q", p.ID, p.Name, p.Description, p.Source)
		}
		if len(p.Description) > 200 || strings.Contains(p.Description, "\n") {
			t.Errorf("%s: description is not short: %q", p.ID, p.Description)
		}
		names := map[string]bool{}
		for _, pr := range p.Profiles {
			if names[pr.Name] {
				t.Errorf("%s: duplicate profile %q", p.ID, pr.Name)
			}
			names[pr.Name] = true
		}
		r, err := renderPreset(p)
		if err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		for _, b := range r.blobs {
			usedBlobs[b.name] = true
		}
	}
	if !seen[DefaultPreset] {
		t.Errorf("default preset %q is not in the catalogue", DefaultPreset)
	}
	if len(Presets()) != 21 {
		t.Errorf("%d presets, the pack has 21 files", len(Presets()))
	}
	for name, file := range BlobFiles() {
		if !usedBlobs[name] {
			t.Errorf("blob %q is not used by any preset", name)
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+\.bin$`).MatchString(file) || file != filepath.Base(file) {
			t.Errorf("blob %q: file %q is not a plain name", name, file)
		}
		if !regexp.MustCompile(`^fake_[a-z0-9_]+$`).MatchString(name) || builtinBlobs[name] {
			t.Errorf("blob name %q", name)
		}
	}
	// The copies are independent of the catalogue.
	ps := Presets()
	ps[0].Profiles[0].Instances[0].Spec = "x"
	if Presets()[0].Profiles[0].Instances[0].Spec == "x" {
		t.Error("Presets returns the catalogue itself")
	}
	BlobFiles()["fake_tls_google"] = "x"
	if BlobFiles()["fake_tls_google"] == "x" {
		t.Error("BlobFiles returns the map itself")
	}
}

func TestPresetSelectionAndCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want string // PresetID
		err  string // substring of the Check error; "" means valid
	}{
		"empty is the default":       {Config{}, DefaultPreset, ""},
		"strategy only is custom":    {Config{Strategy: []string{"--lua-desync=fake"}}, PresetCustom, ""},
		"explicit custom":            {Config{Preset: "custom"}, PresetCustom, ""},
		"custom with strategy":       {Config{Preset: "custom", Strategy: []string{"--lua-desync=fake"}, Ports: []uint16{443}}, PresetCustom, ""},
		"named":                      {Config{Preset: "alt3-hostfakesplit"}, "alt3-hostfakesplit", ""},
		"auto is reserved":           {Config{Preset: "auto"}, PresetAuto, "not available yet"},
		"unknown":                    {Config{Preset: "alt2"}, "alt2", "unknown preset"},
		"upper case is unknown":      {Config{Preset: "ALT2-multisplit"}, "ALT2-multisplit", "unknown preset"},
		"named with strategy":        {Config{Preset: "alt2-multisplit", Strategy: []string{"--lua-desync=fake"}}, "alt2-multisplit", "only with preset"},
		"named with ports":           {Config{Preset: "alt2-multisplit", Ports: []uint16{443}}, "alt2-multisplit", "only with preset"},
		"custom still rejects --new": {Config{Strategy: []string{"--new"}}, PresetCustom, "set by the app"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.cfg.PresetID(); got != tc.want {
				t.Fatalf("PresetID = %q, want %q", got, tc.want)
			}
			err := tc.cfg.Check()
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("Check: %v", err)
			case tc.err != "" && (err == nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("Check = %v, want ErrInvalidConfig mentioning %q", err, tc.err)
			}
		})
	}
	// Build refuses what Check refuses, before anything is rendered.
	if _, err := Build(presetCfg(WinWS, "auto"), uplink(), desyncTable(t)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Build with auto: %v", err)
	}
}

func TestPresetEmptyListIsNothingToRun(t *testing.T) {
	tb := tableOf(t, route.Entry{Domain: "a.example", Route: route.Direct, Source: route.SourceChecked})
	plan, err := Build(presetCfg(WinWS, "alt2-multisplit"), uplink(), tb)
	if err != nil || plan.Path != "" || len(plan.Requires) != 0 {
		t.Fatalf("plan = %+v, err %v", plan, err)
	}
}

func TestFakeDirOf(t *testing.T) {
	for in, want := range map[string]string{
		"C:/app/winws2.exe":     "C:/app/fake",
		`C:\app dir\winws2.exe`: `C:\app dir/fake`,
		"/opt/engine":           "/opt/fake",
		"/engine":               "/fake",
		"winws2.exe":            "fake",
	} {
		if got := fakeDirOf(in); got != want {
			t.Errorf("fakeDirOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderPresetRejectsStrangers(t *testing.T) {
	good := Profile{Name: "p", Proto: TCP, Ports: []uint16{443}, Instances: []Instance{{Spec: "fake:blob={fake_tls_google}"}}}
	for name, mutate := range map[string]func(*Profile){
		"unknown blob":      func(p *Profile) { p.Instances[0].Spec = "fake:blob={fake_tls_nope}" },
		"path as blob":      func(p *Profile) { p.Instances[0].Spec = "fake:blob=C:/x/evil.bin" },
		"file reference":    func(p *Profile) { p.Instances[0].Spec = "fake:blob=@evil.bin" },
		"no ports":          func(p *Profile) { p.Ports = nil },
		"no calls":          func(p *Profile) { p.Instances = nil },
		"unknown transport": func(p *Profile) { p.Proto = "sctp" },
		"space in call":     func(p *Profile) { p.Instances[0].Spec = "fake --new" },
	} {
		t.Run(name, func(t *testing.T) {
			p := good
			p.Instances = append([]Instance(nil), good.Instances...)
			mutate(&p)
			if _, err := renderPreset(Preset{ID: "x", Profiles: []Profile{p}}); err == nil {
				t.Fatal("rendered")
			}
		})
	}
	if _, err := renderPreset(Preset{ID: "x", Profiles: []Profile{good}}); err != nil {
		t.Fatal(err)
	}
}
