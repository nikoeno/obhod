# Desync presets

Ready-made strategies for the desync engine (zapret2: `winws2` on Windows, `nfqws2` on Linux). They are the 21 `general*.bat` files of the zapret1 pack "zapret-discord-youtube 1.10.1" (`winws.exe`, zapret1 syntax), translated by hand into zapret2 Lua calls. The catalogue lives in `presets.go`; this file is the reference for the translation and lists what could not be translated faithfully. Decided 2026-11-01 (stage 1 of 3: engine and config; the UI picker and automatic selection come later, see the end).

**Nothing here was run against a real engine.** `winws2` needs admin and WinDivert, `nfqws2` needs root; the project rules forbid running them. The translation was checked against the zapret2 1.0.5.2 manual (`docs/manual.en.md`, `docs/readme.md`, `blockcheck2.d`) and the Lua sources, and every rendered command line is tested for shape (see "Tests"). Whether a preset helps on a given network is the job of the later automatic stage, and of whoever tries it first.

## What a preset is

A preset is an ordered list of **profiles**. A profile is a match (TCP or UDP, destination ports) and the Lua calls made for it (`Instance`: an optional `--payload` filter and a `--lua-desync` spec). The pack's batch files have nine profiles each; three of them survive (below), so every preset has the same three profiles:

| Profile | Match | What it is in the pack |
|---|---|---|
| `quic` | UDP 443 | the QUIC fake (pack profile 1; the `quic_initial` payload) |
| `alt-ports` | TCP 2053, 2083, 2087, 2096, 8443 | Discord's `discord.media` voice hosts (pack profile 3) |
| `web` | TCP 80, 443 | the general list (pack profile 5); also used for the Google list, see "Merged" |

The renderer, not the preset, owns everything that keeps the engine safe:

- `--hostlist=hostlist.txt` starts **every** profile (the hostlist is the desync-route domains of the route table), so a preset cannot match anything else; a profile without a hostlist (the pack's Discord voice profile, SYN data) is not representable and was left out.
- the interface restriction, the tunnel-address exclusion and the probe-port exclusion (the WinDivert raw filter; the nft rules) are added around the profiles; `--wf-*`, `--new`, `--skip`, `--hostlist*`, `--ipset*`, `--qnum`, `--fwmark`, `--lua-init`, `--uid`, `--user` and `@file` are not in any profile, and the tests assert it for every preset on both engines;
- the intercepted TCP ports are the union of the profiles' TCP ports (`80,443,2053,2083,2087,2096,8443`), the UDP ports are `443`. `desync.ports` is therefore refused for a named preset.

## Payload blobs

zapret2 has no `--dpi-desync-fake-*` options: fake payloads are **blobs**, files loaded with `--blob=<name>:@<file>` into a Lua variable, or the three that are built in (`fake_default_tls`, `fake_default_http`, `fake_default_quic`). Presets name payloads only by a logical name written `{name}` in the call; the renderer replaces it by the name, emits one `--blob` per used payload (sorted by name) and reports the file in `Plan.Requires`. The files are looked up in a `fake` folder next to the engine binary; the file names are fixed in the catalogue (`BlobFiles`). A path or an `@file` in `desync.strategy` is still refused, and a preset that names an unknown blob, a path, or anything but a catalogue/built-in/hex blob does not render (tested).

| Logical name | File in `fake/` | Size | Used by |
|---|---|---|---|
| `fake_tls_google` | `tls_clienthello_www_google_com.bin` | 681 | 15 presets (the pack and the official zapret2 archive hold the same bytes) |
| `fake_quic_google` | `quic_initial_www_google_com.bin` | 1200 | all 21 (QUIC profile; same bytes in the official archive) |
| `fake_tls_max_ru` | `tls_clienthello_max_ru.bin` | 664 | 15 presets (the HTTP fake of most, the TLS fake and multisplit pattern of some) |
| `fake_tls_4pda` | `tls_clienthello_4pda_to.bin` | 284 | 2 presets: `general-multisplit`, `alt10-fake-ts` |
| `fake_stun2` | `stun2.bin` | 120 | 1 preset: `exp-fake-multisplit` (seqovl pattern) |

The default preset (`alt2-multisplit`) needs only the first two. `docs/HUMAN-third-party.md` has the checksums and the to-do for the person who packages the app. The renderers are pure (they do not read the folder); `internal/pc` checks `Plan.Requires` at connect and maps a missing file to `program_missing` like a missing program. The built-in blobs need no file.

## Option by option

zapret1 (`winws.exe`) on the left, what the catalogue uses on the right.

| zapret1 option in the pack | zapret2 | Notes |
|---|---|---|
| `--wf-tcp=80,443,2053,...,%GameFilterTCP%` | `--wf-tcp-out=<union of the profiles' TCP ports>` | outbound only (the project intercepts the uplink outbound only); the game filter port range is not used |
| `--wf-udp=443,19294-19344,50000-50100,%GameFilterUDP%` | `--wf-udp-out=443` | the Discord voice and game ranges are not used |
| `--filter-tcp=P` / `--filter-udp=P` | `--filter-tcp=P` / `--filter-udp=P` per profile | |
| `--filter-l7=quic` (EXP only) | `--payload=quic_initial` | equal for the UDP 443 profile |
| `--filter-l7=discord,stun[,unknown]`, `--filter-l3=ipv4` | not used | they belong to profiles that are left out |
| `--hostlist`, `--hostlist-domains`, lists | the renderer's `--hostlist=hostlist.txt` in every profile | see "Lists" |
| `--hostlist-exclude*`, `--ipset-exclude*` | not needed | the hostlist holds only domains the checker routed to desync; the tunnel address and the probe ports are excluded by the raw filter / nft rules |
| `--ipset=ipset-all.txt` profiles | not used | an IP-address match; `ipset-all.txt` is the 18-byte placeholder `203.0.113.113/32` in the pack unless its optional "loaded" mode is switched on, so the profiles are inert by default |
| `--new` | `--new` (the renderer) | |
| `--dpi-desync=fake` | one `--lua-desync=fake:blob=...` per payload type, each after `--payload=<type>` | the pack's `fake-tls` / `fake-http` / `fake-quic` options are per payload type; zapret2 has instances |
| `--dpi-desync-fake-tls=F` | `--payload=tls_client_hello`, `fake:blob={name}` | |
| `--dpi-desync-fake-tls=A --dpi-desync-fake-tls=B` | the last file (B) | **not faithful**, see N1 |
| `--dpi-desync-fake-tls-mod=rnd,dupsid,sni=X` | `tls_mod=rnd,dupsid,sni=X` on `fake:blob=fake_default_tls` | the pack's built-in default TLS fake becomes the zapret2 built-in one |
| `--dpi-desync-fake-tls-mod=none` | no `tls_mod` | |
| `--dpi-desync-fake-http=F` | `--payload=http_req`, `fake:blob={name}` | the pack uses a TLS ClientHello file as the HTTP fake; kept as is |
| `--dpi-desync-fake-quic=F` | `--payload=quic_initial`, `fake:blob={fake_quic_google}` | |
| `--dpi-desync-fake-unknown`, `-unknown-udp`, `-discord`, `-stun` | not used | profiles that are left out |
| `--dpi-desync-repeats=N` | `repeats=N` on the fakes | applied to fakes only, as zapret2's `hostfakesplit`/`fakedsplit` do; not to the real pieces (N6) |
| `--dpi-desync-fooling=ts` | `tcp_ts=-600000` | the zapret1 default timestamp increment (`--dpi-desync-ts-increment`), from memory of the zapret1 docs; zapret2's own examples use `-1000` (N5) |
| `--dpi-desync-fooling=md5sig` | `tcp_md5` | |
| `--dpi-desync-fooling=badseq`, `--dpi-desync-badseq-increment=N` | `tcp_seq=N` (default `-10000`) | the zapret2 readme: "badseq without parameters applied -10000" |
| `--dpi-desync=multisplit`, `-split-pos`, `-split-seqovl`, `-split-seqovl-pattern` | `multisplit:pos=..:seqovl=..:seqovl_pattern={blob}` | exact; `pos` markers (`1`, `2`, `sniext+1`, `midsld`) are the same names in zapret2 |
| `--dpi-desync=multidisorder`, `-split-pos` | `multidisorder_legacy:pos=..` | `_legacy` is the zapret2 function documented as "fully compatible with nfqws1" |
| `--dpi-desync=fakedsplit`, `-fakedsplit-pattern=0x00` | `fakedsplit:pos=..:pattern=0x00` | fooling and repeats go to the fake pieces only, as in nfqws1 |
| `--dpi-desync=hostfakesplit`, `-hostfakesplit-mod=host=H,altorder=1` | `hostfakesplit:host=H:nofake1` | `altorder=1` has no zapret2 argument; `nofake1` is the closest (N3) |
| `--dpi-desync=syndata` | not used | N2 |
| `--ip-id=zero` | not used | only on the Google profile (N4) |
| `--dpi-desync-any-protocol=1`, `--dpi-desync-cutoff=nN` | not used; would be `--payload=all` and `--out-range=-nN` | only in the game/unknown-UDP profiles; the zapret2 readme: cutoff is now `--out-range`, `-` is inclusive |

## What could not be translated faithfully

Everything below is a deliberate choice, not an oversight. The first group applies to all 21 presets, the second to the ones named.

**All presets**

- **Discord voice and STUN UDP profile** (`--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun`, the `ACTIVE_DISCORD_UDP.bin` fake): dropped. It has no hostlist; UDP voice has no host name, and zapret2 never selects a profile that has a hostlist when the host is unknown (manual, "Filtering by lists"). A hostlist-less profile would let a preset act outside the hostlist, which the renderer forbids. Needs an IP set (see "Lists").
- **Game filter profiles** (`%GameFilterTCP%`, `%GameFilterUDP%`, `ACTIVE_GAME_UDP.bin`, `--dpi-desync-any-protocol`, `--dpi-desync-cutoff`) and **the `ipset-all` profiles**: dropped. They match IP addresses, and the pack ships them off (`GameFilter*` = port 12, `ipset-all.txt` = a placeholder).
- **Merged Google profile.** The pack has a separate TCP 443 profile for `list-google.txt` (YouTube and Google hosts) before the general one, with `--ip-id=zero`. The renderer has one hostlist, so the `web` profile serves both and the Google variant is lost. The delta per preset is in the next section.
- **Hostlist matching of the pack's lists** differs from ours by design, see "Lists".

**Per preset** (the "Note" column of the catalogue, repeated here)

- **N1, concatenated fakes.** In the pack `--dpi-desync-fake-tls=stun.bin --dpi-desync-fake-tls=tls_clienthello_*.bin` builds one payload from both files (winws joins repeated fake options; from the zapret1 docs, not verifiable offline). A zapret2 call takes one blob. The presets use the TLS part only: `alt-fakedsplit`, `alt4-fake-multisplit`, `alt10-fake-ts`, `alt11-fake-multisplit`, `alt12-fake-multisplit-google`, `simple-fake-alt`, `simple-fake-alt2` (the prefix files are `stun.bin` and `stun2.bin`). `faketls-auto` uses `0x00000000` followed by `^!` (a built-in payload with a modifier I cannot interpret); it uses the built-in fake TLS with the SNI mod. A faithful version could join blobs with an inline `--lua-init` owned by the renderer; not done because it cannot be tried here (order of `--blob` and `--lua-init` is unverified).
- **N2, `syndata`** (`alt5-multidisorder`; `alt7` has it only in the ipset profiles): data in the SYN packet has no host name yet, so no hostlist profile can select it, and the pack applies it to every site. Left out; `alt5-multidisorder` is therefore only the disorder part, for the hostlist.
- **N3, `altorder=1`** (`alt3-hostfakesplit`): zapret2's `hostfakesplit` has no such argument. The zapret2 readme ports `fakedsplit`'s `altorder=1` to `nofake1`, so `nofake1` is used here too. **Unverified.** `alt3` is one of the presets the user reports working in the pack; if the translation does not behave like it, this is the first suspect (try without `nofake1`, or with `disorder_after`).
- **N4, `--ip-id=zero`**: only on the pack's Google profile, which is merged away. It would be `ip_id=zero` on the calls.
- **N5, fooling increments**: `ts` is `tcp_ts=-600000`. The zapret2 manual only says "tcp_ts: positive or negative offset" and its blockcheck uses `-1000`; either makes the server reject the fake by timestamp. The exact value is a guess from the zapret1 default.
- **N6, `repeats` and the real pieces**: in the pack `--dpi-desync-repeats` may also repeat the real split pieces (zapret1 detail not verifiable here); the presets repeat fakes only (`alt4`, `alt11`, `alt12`, `exp`, `faketls-auto`, `faketls-auto-alt2` and `faketls-auto-alt3` have `repeats` next to a multisplit or disorder call).
- **`alt12-fake-multisplit-google`** renders the same as `alt11-fake-multisplit`: the pack's two files differ in the Discord voice profile (`stun.bin` as an extra Discord fake) and in the Google profile (host fake split instead of fake and multisplit), and both are left out. It is kept so that all 21 files have an id; the later stages can hide or reuse it.
- **`exp-fake-multisplit`**: the pack's other changes are in the left-out UDP and game profiles; the QUIC profile is the same (`--filter-l7=quic` instead of `--filter-udp=443`).
- **`faketls-auto-*`** use the built-in fake TLS (`fake_default_tls`) with `tls_mod=rnd,dupsid,sni=www.google.com`, as the pack does (`--dpi-desync-fake-tls-mod` without a file). The pack's default modifiers for a built-in fake when no `fake-tls-mod` is given (`alt8-fake-badseq`: the pack says `none`) are taken as "none".
- **Behaviour of fooling on `multisplit`.** In zapret2 a `tcp_ts` or `tcp_seq` argument on `multisplit` would be applied to the real segments too (the manual: "fooling" is a standard argument of `multisplit`; for `fakedsplit`/`hostfakesplit` it is applied to the fakes only). The presets therefore put fooling on the `fake` calls and on `fakedsplit`/`hostfakesplit`, never on `multisplit`/`multidisorder_legacy`, which matches nfqws1 (fooling only for fakes).

## The presets

Every preset has the profiles `quic`, `alt-ports`, `web` (above). "QUIC" is the repeat count of the QUIC fake. `ts`, `seq(N)`, `md5` are the fooling arguments above; `G`, `M`, `4pda`, `T` are the payloads `fake_tls_google`, `fake_tls_max_ru`, `fake_tls_4pda` and the built-in `fake_default_tls`. "Google delta" is what the merged pack Google profile did differently on TCP 443 (besides `--ip-id=zero`, which every preset loses; `ip-id` alone is written "ip-id").

| Id | Display name | Source file | QUIC | `alt-ports` | `web` | Google delta lost | Note |
|---|---|---|---|---|---|---|---|
| `alt2-multisplit` (**default**) | ALT2 (multisplit) | `general (ALT2).bat` | 6 | `multisplit pos=2 seqovl=652 pat=G` | same | ip-id | |
| `alt3-hostfakesplit` | ALT3 (host fake split) | `general (ALT3).bat` | 6 | `fake T r1 ts mod sni=www.google.com`; `hostfakesplit host=www.google.com nofake1 ts` | `fake T mod sni=ya.ru ts`; http `fake M ts`; `hostfakesplit host=ya.ru nofake1 ts` | host `www.google.com` instead of `ya.ru` | N3 |
| `alt-fakedsplit` | ALT (fake and fake split) | `general (ALT).bat` | 6 | `fake G r6 ts`; `fakedsplit pos=2 pattern=0x00 r6 ts` | the same plus http `fake M r6 ts` | no http fake | N1 |
| `general-multisplit` | General (multisplit) | `general.bat` | 6 | `multisplit pos=1 seqovl=681 pat=G` | `multisplit pos=1 seqovl=568 pat=4pda` | seqovl 681 with G instead of 568 with 4pda | |
| `alt4-fake-multisplit` | ALT4 (fake and multisplit) | `general (ALT4).bat` | 6 | `fake G r6 seq(1000)`; `multisplit pos=2` | the same plus http `fake M r6 seq(1000)` | no http fake | N1 |
| `alt5-multidisorder` | ALT5 (disorder) | `general (ALT5).bat` | 6 | `multidisorder_legacy pos=2` | same | none (no Google profile) | N2 |
| `alt6-multisplit-681` | ALT6 (multisplit, long decoy) | `general (ALT6).bat` | 6 | `multisplit pos=1 seqovl=681 pat=G` | same | ip-id | |
| `alt7-multisplit-sniext` | ALT7 (multisplit at SNI) | `general (ALT7).bat` | 6 | `multisplit pos=2,sniext+1 seqovl=679 pat=G` | same | ip-id | |
| `alt8-fake-badseq` | ALT8 (fake, shifted sequence) | `general (ALT8).bat` | 6 | `fake T r6 seq(2)` | the same plus http `fake M r6 seq(2)` | no http fake | |
| `alt9-hostfakesplit-ozon` | ALT9 (host fake split, ozon.ru) | `general (ALT9).bat` | 6 | `hostfakesplit host=www.google.com r4 ts` | `hostfakesplit host=ozon.ru r4 ts md5` | host `www.google.com`, no md5 | |
| `alt10-fake-ts` | ALT10 (fake, stale timestamp) | `general (ALT10).bat` | 6 | `fake G r6 ts` | `fake 4pda r6 ts`; http `fake M r6 ts` | G instead of 4pda, no http fake | N1 |
| `alt11-fake-multisplit` | ALT11 (fake and multisplit) | `general (ALT11).bat` | 11 | `fake G r8 ts`; `multisplit pos=1 seqovl=681 pat=G` | `fake M r8 ts`; http `fake M r8 ts`; `multisplit pos=1 seqovl=664 pat=M` | G/681 instead of M/664, no http fake | N1, N6 |
| `alt12-fake-multisplit-google` | ALT12 (fake and multisplit, Google variant) | `general (ALT12).bat` | 11 | as ALT11 | as ALT11 | **host fake split `host=www.google.com ts` instead of fake and multisplit** | N1, N6, same output as ALT11 |
| `exp-fake-multisplit` | EXP (experimental) | `general (EXP).bat` | 11 | `fake G r8 ts`; `multisplit pos=1 seqovl=681 pat=G` | `fake M r4 ts`; http `fake M r4 ts`; `multisplit pos=1 seqovl=480 pat=stun2` | **host fake split `host=www.google.com ts`** | N6 |
| `faketls-auto` | Fake TLS auto (disorder) | `general (FAKE TLS AUTO).bat` | 11 | `fake T r11 seq(-10000) mod sni=www.google.com`; `multidisorder_legacy pos=1,midsld` | the same plus http `fake M r11 seq(-10000)` | no http fake | N1 (`0x00000000` + `^!`) |
| `faketls-auto-alt-fakedsplit` | Fake TLS auto ALT (fake split) | `general (FAKE TLS AUTO ALT).bat` | 11 | `fake T r8 seq(2) mod`; `fakedsplit pos=1 r8 seq(2)` | the same plus http `fake M r8 seq(2)` | no http fake | |
| `faketls-auto-alt2-multisplit` | Fake TLS auto ALT2 (multisplit) | `general (FAKE TLS AUTO ALT2).bat` | 11 | `fake T r8 seq(10000000) mod`; `multisplit pos=1 seqovl=681 pat=G` | the same plus http `fake M r8 seq(10000000)` | no http fake | N6 |
| `faketls-auto-alt3-multisplit-ts` | Fake TLS auto ALT3 (multisplit, timestamp) | `general (FAKE TLS AUTO ALT3).bat` | 11 | `fake T r8 ts mod`; `multisplit pos=1 seqovl=681 pat=G` | the same plus http `fake M r8 ts` | no http fake | N6 |
| `simple-fake` | Simple fake | `general (SIMPLE FAKE).bat` | 6 | `fake G r6 ts` | the same plus http `fake M r6 ts` | **host fake split `host=www.google.com ts` instead of a fake** | |
| `simple-fake-alt` | Simple fake ALT (shifted sequence) | `general (SIMPLE FAKE ALT).bat` | 6 | `fake G r6 seq(2)` | the same plus http `fake M r6 seq(2)` | no http fake | N1 |
| `simple-fake-alt2` | Simple fake ALT2 (stale timestamp) | `general (SIMPLE FAKE ALT2).bat` | 6 | `fake G r6 ts` | `fake M r6 ts`; http `fake M r6 ts` | G instead of M, no http fake | N1 |

### Why `alt2-multisplit` is the default

The user reports ALT2, ALT3 and ALT working on their (Russian) network. ALT2 is the best default of the three, for reasons that hold for any network:

- it is the **most faithful translation**: `multisplit` with `seqovl` maps one to one, there is no fake payload to join (N1), no `altorder` (N3), no fooling increment (N5) and no repeat semantics (N6). Its Google profile is identical to its general one except `--ip-id=zero`, so merging loses nothing else;
- it sends **no fake packets** and uses no TCP option tricks (timestamp, MD5, sequence shifts): the fakes of the other presets can be dropped or rewritten by NAT boxes and make strict servers answer with resets, the seqovl decoy is inside a real segment;
- it needs only two payload files (`fake_tls_google`, `fake_quic_google`), both byte-identical to files in the official zapret2 archive;
- it has the same split parameters for the Discord voice ports, Google/YouTube and everything else, so the default behaves the same for all desync sites.

ALT3 (translation uncertain, N3) and ALT (stun prefix lost, N1) are the first alternatives to try.

## Lists

The pack matches by list files. The presets match by the app's own hostlist (the route table's desync-route domains), so a preset acts on a site exactly when the checker has routed it to desync (or the user said so). Nothing was imported. What the pack's profiles rely on, and how it maps:

- **`discord.media`** (profile 3, TCP 2053 2083 2087 2096 8443, `--hostlist-domains`): it is in the app's starter list (`candidates.txt`), so the checker can route it to desync and the `alt-ports` profile then covers it. If the checker routes it elsewhere, the profile never fires.
- **`list-general.txt`** (50 domains: Discord, Cloudflare services, 7TV, FrankerFaceZ, BetterTTV, `discord-attachments-uploads-prd.storage.googleapis.com`, ...): 7 of the 50 are covered by the starter list (a listed domain covers its subdomains). The covered ones are `discord.com`, `discord.gg`, `discord.media`, `discordapp.com`, `discordapp.net`, `discordcdn.com` and `discordstatus.com`. The rest (`discord.app`, `discord.co`, `discordsays.com`, `cloudflarestream.com`, `cloudfront.net`, `dis.gd`, ...) would need to be in `candidates.txt` and be found blocked by the checker. **Proposal:** the user (or a later stage) adds those that matter to their own `candidates.txt`; no change to the code.
- **`list-google.txt`** (20 domains: YouTube, `googlevideo.com`, `ytimg.com`, `ggpht.com`, `play.google.com`, `google.ru`, `*.googleapis.com` endpoints, `stable.dl2.discordapp.net`): 9 are covered by the starter list. These hosts had their own profile in the pack. **Proposal (needs a decision):** a second hostlist file `hostlist-google.txt` (the desync domains inside a fixed Google set), a `Profile.Hosts` field selecting it, `--hostlist-exclude=hostlist-google.txt` on the profiles that serve everything else, and the pack's Google profile for each preset. It would restore the Google deltas in the table and `ip_id=zero`, and make `alt12-fake-multisplit-google` and `exp-fake-multisplit` differ from their neighbours. It adds one file to `Plan.Files` (the app already writes every entry) and about 60 lines. Not done: the fixed Google set is a list from the pack, and the task said to propose rather than import.
- **`list-exclude.txt`** (112 domains the pack never touches: Yandex, VK, banks, government sites, Microsoft, Epic, Riot, ...): not needed. A domain on that list is not blocked, so the checker does not route it to desync; `candidates.txt` does not contain it either.
- **`ipset-exclude*.txt`**: private ranges and `203.0.113.113`; the raw filter / nft rules exclude the tunnel address and the engine only sees the uplink's global destinations (`--wf-filter-lan` is on by default in winws2).
- **`ipset-all.txt`** (stub by default; 556 KB list of service addresses in its optional "loaded" mode) and the **game filter**: IP-address matches, outside the hostlist model. **Proposal:** an optional, separate `desync.ipsets` feature (user-supplied file names only) if the Discord voice and game profiles are wanted; it needs the renderer to own a second profile family with `--ipset` and the same exclusions, and a decision on how much the "hostlist-only" rule may bend. The Discord voice UDP fake (`ACTIVE_DISCORD_UDP.bin`) is wanted for that.

## Tests

`preset_test.go` renders every preset for `winws2` and `nfqws2` through `Build` and checks: `Config.Check` passes; every argument is either renderer-owned or a profile filter/payload/Lua call (a whitelist, so no forbidden option can slip in); exactly one `--hostlist=hostlist.txt` per profile and `--new` between them; the WinDivert raw filter has the interface, the tunnel exclusion and the probe-port clause (and nothing else adds a second one); the nft rules have `oifname`, the early `return`s and the `udp dport` rule; `--uid=0:0`; every blob a call names is declared by a `--blob` option from the catalogue or built in; `Plan.Requires` is exactly the declared files; the output is deterministic. Golden command lines (`testdata/preset_*.golden`): `alt2-multisplit` (Windows and Linux), `alt3-hostfakesplit`, `alt-fakedsplit`, `faketls-auto`. Further tests cover the catalogue (ids, names, sources, 21 entries, default present, every blob used), `desync.preset` validation (`custom`, `auto` refused, unknown, strategy/ports with a named preset), and that a preset which names a path as a blob does not render.

## Planned: automatic selection

Not implemented. `desync.preset: auto` is reserved: `Config.Check` refuses it with "not available yet".

**Stage 2 (engine side).** For one network, run the presets one after the other and keep the first or best that works:

1. Take the domains the checker found failing on `direct` (the same set that makes a domain `desync` or `tunnel` candidate).
2. For each preset in a fixed order (default first, then by the user's earlier success on similar networks), build the plan for exactly those domains, start `winws2`/`nfqws2` through procman, wait until the engine is up, and probe the domains through the **real direct dialer** of the app. The engine mangles the physical path transparently, so the existing direct probes (`analyzer` stages tcp/tls/http/bulk) measure the effect without any special channel. Stop the engine; record per preset how many domains passed.
3. Keep the first preset that makes all (or the largest share of) the failing domains pass, store the choice per network label (the identity the checker already keeps per network, never the public IP) in `Paths.Data`, and use it as the effective preset (`PresetID` stays a pure function of the config; the stored choice would be applied by `internal/pc` as an override when the config says `auto`).
4. Re-run on a new network, on a stored preset that stops working (the monitor's stall reports), or by user request. Probe politely (low concurrency, jitter, a few domains per preset), never against censorship infrastructure; the whole search has a time and attempt budget and falls back to the tunnel route for what no preset fixes.

The sequencing needs an admin session (the engine) and Linux needs the nft table per candidate plan (the `Setup` commands change with the ports, `internal/pc/nft.go` already detects a stale table). On Linux the search therefore needs either the user to apply one table that covers every preset's ports (the union is `tcp 80,443,2053,2083,2087,2096,8443` and `udp 443`, the same for all 21 presets, so one `Setup` serves all) or a lab namespace.

**Stage 3 (UI).** The Settings page lists `Presets()` (id, display name, one-sentence description, a flag for the presets whose translation differs from the pack) next to an "Automatic" entry, and writes `desync.preset`. The control API gets the list and the active choice (additive).

## Open questions

1. Google profile: implement the `hostlist-google.txt` mechanism above? (Changes: `Profile.Hosts`, one more file in `Plan.Files`, a hostlist-exclude on the other profiles, a Google domain set in code.)
2. Discord voice/STUN and game profiles: do you want an opt-in IP-set feature, and how far may it bend "hostlist only"?
3. `altorder=1` of ALT3: confirm the `nofake1` choice on a real run, or give me the zapret1 semantics.
4. Ship `fake/` in the app package: the manifest needs entries (see `docs/HUMAN-third-party.md`).
5. Linux: one `Setup` covers every preset (same ports); should `check-config` print it once instead of per plan?
