# Recovery cheatsheet: the internet stopped working after `splitter run`

Keep a copy of this file where you can open it offline (phone, printed, another PC).
Run everything in an **elevated PowerShell** (right click, Run as administrator).
After each step, check whether the internet is back. Stop when it is.

## 0. Before you start experiments

- Have a second connection ready: a phone hotspot or another device.
- Keep an elevated PowerShell window open, with step 1 already typed.
- Do not close that window while testing.

## 1. Kill everything (fixes it in most cases)

```powershell
Stop-Process -Name splitter, sing-box, winws2 -Force -ErrorAction SilentlyContinue
```

If that window is gone: Task Manager, Details tab, end `splitter.exe`, `sing-box.exe`, `winws2.exe`.

Wait about 5 seconds and test (`ping 1.1.1.1`, then `ping example.com`).

- Pings to `1.1.1.1` work but names do not resolve: it is DNS, go to step 4.
- Nothing works: continue.

## 2. Remove the TUN adapter and leftover routes

The adapter is named `splitter` by default (`singbox.tun.name` in `app.yaml`).

```powershell
Get-NetAdapter | Where-Object { $_.Name -like "splitter*" -or $_.InterfaceDescription -like "*Wintun*" }
Disable-NetAdapter -Name "splitter" -Confirm:$false
route print -4
```

Look at the routing table. Entries whose gateway or interface is `198.19.0.x` are ours:

```powershell
route delete <destination>
```

Your real default route is the `0.0.0.0` line with your router address as gateway. Do not delete that one.

## 3. Unload the WinDivert packet driver

Only needed if packets still seem to be dropped after step 1.

```powershell
sc.exe query type= driver | findstr /i divert
sc.exe stop WinDivert
sc.exe delete WinDivert
```

Use the exact service name that the first command printed (it can be `WinDivert14`). A reboot also unloads it.

## 4. DNS and network stack reset

```powershell
ipconfig /flushdns
ipconfig /release
ipconfig /renew
```

If it is still broken (reboot afterwards):

```powershell
netsh winsock reset
netsh int ip reset
```

## 5. Reboot

`splitter` does not install a service and does not change system DNS, proxy, firewall or hosts settings. A reboot returns the machine to its normal state.

## 6. Check what is left

```powershell
Get-Process splitter, sing-box, winws2 -ErrorAction SilentlyContinue
Get-NetAdapter
ipconfig /all
```

Nothing from the first command should be listed.

## Files and logs (for later, when the internet is back)

- Logs: the `logs` directory of the app (`paths.Default("splitter")`, or `paths.root` from `app.yaml`), file `splitter.log`.
- Generated configs: the `run` directory, `sing-box.json`. It contains the Clash API secret, do not share it.
- Secrets: the `secrets` directory, `tunnel.json`. It holds your VPS credentials, never post it.

## Linux

```bash
sudo pkill -9 splitter sing-box nfqws2
sudo nft delete table inet splitter
ip route
ip link
```

Only the `inet splitter` table is ours. Never run `nft flush ruleset`.
If the interface `splitter` is still listed by `ip link`: `sudo ip link delete splitter`.

## Notes

- These commands are the standard ones for these tools. The real run has not been tested yet, so steps 2 and 3 are the part most likely to need adjusting.
- Exit codes of `splitter`: 0 ok, 1 config or usage error, 2 needs administrator rights or a manual step (the message says which), 3 runtime failure.
