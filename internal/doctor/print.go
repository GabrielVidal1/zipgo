package doctor

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Print writes a human-readable report. Findings are grouped by the site they
// belong to, so the output reads as "here is what is wrong with this host"
// rather than a flat lint dump.
func Print(w io.Writer, rep Report, domainsDir string) {
	fmt.Fprintf(w, "🔎  Checking %s\n", domainsDir)
	fmt.Fprintf(w, "📁  Domains: %d  Sites: %d", rep.Domains, rep.Sites)
	if rep.Disabled > 0 {
		fmt.Fprintf(w, "  (%d disabled)", rep.Disabled)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	if len(rep.Findings) == 0 {
		fmt.Fprintln(w, "✅  No problems found.")
		return
	}

	// Group by host, keeping hostless findings (bad domain folders) first.
	byHost := map[string][]Finding{}
	var order []string
	for _, f := range rep.Findings {
		key := f.Host
		if _, seen := byHost[key]; !seen {
			order = append(order, key)
		}
		byHost[key] = append(byHost[key], f)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if (order[i] == "") != (order[j] == "") {
			return order[i] == ""
		}
		return order[i] < order[j]
	})

	for _, host := range order {
		label := host
		if label == "" {
			label = "(no host)"
		}
		fmt.Fprintf(w, "  %s\n", label)
		for _, f := range byHost[host] {
			fmt.Fprintf(w, "    %s %s\n", f.Level.Icon(), f.Msg)
			fmt.Fprintf(w, "       ↳ %s\n", f.Path)
			if f.Hint != "" {
				for _, line := range wrap(f.Hint, 68) {
					fmt.Fprintf(w, "         %s\n", line)
				}
			}
		}
		fmt.Fprintln(w)
	}

	errs, warns := rep.Errors(), rep.Warnings()
	switch {
	case errs > 0:
		fmt.Fprintf(w, "❌  %s, %s\n", plural(errs, "error"), plural(warns, "warning"))
	case warns > 0:
		fmt.Fprintf(w, "⚠️   %s\n", plural(warns, "warning"))
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// wrap breaks s into lines of at most width characters, on word boundaries.
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(lines, line)
}
