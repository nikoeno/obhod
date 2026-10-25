package pc

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	nftOifname = regexp.MustCompile(`oifname "[^"]+"`)
	nftReturn  = regexp.MustCompile(`\{ ([^}]*) \} return`)
)

// nftMissing compares the listing of the "inet splitter" table with the setup
// commands of the current plan and returns the uplink match and the excluded
// addresses and ports the listing lacks. nft prints a host prefix (/32,
// /128) as a bare address, so those are compared without the length.
// Addresses and ports are compared as whole tokens of the listing (so
// 10.0.0.1 is not found inside 10.0.0.10), the uplink as its quoted match.
// This is enough to notice an uplink or tunnel address change; it does not
// prove the rules are otherwise intact.
func nftMissing(listing string, setup []string) []string {
	tokens := map[string]bool{}
	for _, tok := range strings.FieldsFunc(listing, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == '{' || r == '}'
	}) {
		tokens[tok] = true
	}
	var missing []string
	for _, cmd := range setup {
		if m := nftOifname.FindString(cmd); m != "" && !strings.Contains(listing, m) {
			missing = append(missing, m)
		}
		sm := nftReturn.FindStringSubmatch(cmd)
		if sm == nil {
			continue
		}
		for _, item := range strings.Split(sm[1], ", ") {
			item = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(item), "/32"), "/128")
			if item != "" && !tokens[item] {
				missing = append(missing, item)
			}
		}
	}
	return missing
}

// ipRuleSpec is the selector and action of one `ip rule add` command.
type ipRuleSpec struct {
	prio       uint64
	mark, mask uint64
	table      string
}

// parseIPRuleAdd reads `ip rule add fwmark V[/M] lookup T priority P`. The
// words may come in any order; anything else makes the command unknown (ok is
// false). A mark without a mask has the full mask, as in the kernel.
func parseIPRuleAdd(cmd string) (spec ipRuleSpec, ok bool) {
	f := strings.Fields(cmd)
	if len(f) < 3 || f[0] != "ip" || f[1] != "rule" || f[2] != "add" {
		return spec, false
	}
	var haveMark, haveTable, havePrio bool
	for i := 3; i+1 < len(f); i += 2 {
		v := f[i+1]
		var err error
		switch f[i] {
		case "fwmark":
			spec.mark, spec.mask, err = parseMark(v)
			haveMark = err == nil
		case "lookup", "table":
			spec.table, haveTable = normTable(v), true
		case "priority", "pref":
			spec.prio, err = strconv.ParseUint(v, 10, 32)
			havePrio = err == nil
		default:
			return spec, false
		}
		if err != nil {
			return spec, false
		}
	}
	return spec, haveMark && haveTable && havePrio && len(f)%2 == 1
}

// parseMark parses "V" or "V/M" (numbers in any base Go accepts: 0x..., 0...).
func parseMark(s string) (mark, mask uint64, err error) {
	v, m, hasMask := strings.Cut(s, "/")
	if mark, err = strconv.ParseUint(v, 0, 32); err != nil {
		return 0, 0, err
	}
	mask = 0xffffffff
	if hasMask {
		if mask, err = strconv.ParseUint(m, 0, 32); err != nil {
			return 0, 0, err
		}
	}
	return mark, mask, nil
}

// normTable maps the numeric id of the main table to its name.
func normTable(t string) string {
	if t == "254" {
		return "main"
	}
	return t
}

// ipRuleMissing returns the `ip rule add` commands of setup that the listing of
// `ip rule show` lacks. A listing line is "<priority>:" followed by words; it
// matches a command when the priority, the mark and mask (compared as numbers,
// so 0x40000000 and 0x040000000 are equal and spacing does not matter) and the
// table (main is also 254) are equal and the line has no other selector or
// action ("from all" is the only filler accepted): a rule that also matches
// "iif eth0", is negated, or has suppress_prefixlength, goto or unreachable does
// not count. Commands that are not `ip rule add` are ignored.
func ipRuleMissing(listing string, setup []string) []string {
	var have []ipRuleSpec
	for _, line := range strings.Split(listing, "\n") {
		if s, ok := parseIPRuleLine(line); ok {
			have = append(have, s)
		}
	}
	var missing []string
	for _, cmd := range setup {
		want, ok := parseIPRuleAdd(cmd)
		if !ok {
			continue
		}
		found := false
		for _, h := range have {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, cmd)
		}
	}
	return missing
}

// parseIPRuleLine parses one `ip rule show` line that consists only of
// "<prio>: [from all] fwmark V[/M] lookup T".
func parseIPRuleLine(line string) (spec ipRuleSpec, ok bool) {
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasSuffix(f[0], ":") {
		return spec, false
	}
	var err error
	if spec.prio, err = strconv.ParseUint(strings.TrimSuffix(f[0], ":"), 10, 32); err != nil {
		return spec, false
	}
	var haveMark, haveTable bool
	for i := 1; i < len(f); {
		switch {
		case f[i] == "from" && i+1 < len(f) && f[i+1] == "all":
			i += 2
		case f[i] == "fwmark" && i+1 < len(f) && !haveMark:
			if spec.mark, spec.mask, err = parseMark(f[i+1]); err != nil {
				return spec, false
			}
			haveMark = true
			i += 2
		case f[i] == "lookup" && i+1 < len(f) && !haveTable:
			spec.table, haveTable = normTable(f[i+1]), true
			i += 2
		default:
			return spec, false
		}
	}
	return spec, haveMark && haveTable
}
