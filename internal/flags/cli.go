package flags

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"zipgo/internal/deploy"
	"zipgo/internal/password"
	"zipgo/internal/sites"
)

// Usage is the `zipgo flag` help text.
const Usage = `Usage: zipgo flag [ls] <host> [--json]
       zipgo flag on|off <host> <name>...
       zipgo flag add <host> <name> [--on] [--description "…"] [--path /p]...
       zipgo flag rm <host> <name>...

Feature flags: switches a site's own code reads from /__zipgo/flags.json
({"flags": {"coop": false}}) to show or hide what isn't ready for everyone.
While a flag is off, zipgo also answers 404 on its --path prefixes, so a
feature that lives on pages of its own can't be reached by URL either.

They live in the site's .zipgoconfig.json "flags" block, which zipgo
hot-reloads; the back-office switches the same flags, and a deploy never
changes them (a build may declare new ones, off unless it says on).

  ls <host>        list the site's flags (the default command)
      --json       as JSON
  on|off <host> <name>...  switch flags on or off
  add <host> <name>        create a flag, or update one (its switch is kept
                           unless --on/--off is given)
      --on | --off         its switch (default for a new flag: off)
      --description T      what the back-office shows next to it
      --path /p            a path prefix closed while it is off (repeatable;
                           --path "" clears them)
  rm <host> <name>...      delete flags

Where: --ssh/--target user@host:/base or --dir <domains folder>, as for
zipgo password.

Examples:
  zipgo flag add ballance.game.example.com coop --description "Co-op levels"
  zipgo flag add spore.example.com arena --path /editor/arena
  zipgo flag on ballance.game.example.com coop
`

// Options is a parsed `zipgo flag` invocation.
type Options struct {
	Verb        string // ls | on | off | add | rm
	Host        string
	Names       []string
	On          *bool
	Description *string
	Paths       []string // nil = not given
	JSON        bool
	SSH         string
	Dir         string
}

// ParseArgs parses everything after `zipgo flag`.
func ParseArgs(args []string) (Options, error) {
	var o Options
	if len(args) == 0 {
		return o, errors.New("missing host")
	}
	switch args[0] {
	case "ls", "list", "status":
		o.Verb, args = "ls", args[1:]
	case "on", "off", "add":
		o.Verb, args = args[0], args[1:]
	case "rm", "remove", "delete":
		o.Verb, args = "rm", args[1:]
	default:
		if strings.HasPrefix(args[0], "-") || !strings.Contains(args[0], ".") {
			return o, fmt.Errorf("unknown command %q: use ls, on, off, add or rm", args[0])
		}
		o.Verb = "ls"
	}
	value := func(i *int, flag string) (string, error) {
		if k, v, ok := strings.Cut(args[*i], "="); ok && strings.HasPrefix(k, "--") {
			return v, nil
		}
		*i++
		if *i >= len(args) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, _, _ := strings.Cut(a, "=")
		var err error
		switch flag {
		case "--on", "--off":
			on := flag == "--on"
			o.On = &on
		case "--description", "--desc":
			var d string
			if d, err = value(&i, flag); err == nil {
				o.Description = &d
			}
		case "--path":
			var p string
			if p, err = value(&i, flag); err == nil {
				if o.Paths == nil {
					o.Paths = []string{}
				}
				if strings.TrimSpace(p) != "" {
					o.Paths = append(o.Paths, p)
				}
			}
		case "--json":
			o.JSON = true
		case "--ssh", "--target":
			o.SSH, err = value(&i, flag)
		case "--dir":
			o.Dir, err = value(&i, flag)
		default:
			if strings.HasPrefix(a, "-") {
				return o, fmt.Errorf("unknown flag %q", a)
			}
			if o.Host == "" {
				o.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a)), ".")
			} else {
				o.Names = append(o.Names, a)
			}
		}
		if err != nil {
			return o, err
		}
	}
	return o, o.validate()
}

func (o Options) validate() error {
	switch {
	case o.Host == "":
		return fmt.Errorf("%s needs a host", o.Verb)
	case o.Verb == "ls" && len(o.Names) > 0:
		return fmt.Errorf("unexpected argument %q", o.Names[0])
	case o.Verb != "ls" && len(o.Names) == 0:
		return fmt.Errorf("%s needs a flag name, e.g. zipgo flag %s %s coop", o.Verb, o.Verb, o.Host)
	case o.Verb == "add" && len(o.Names) > 1:
		return errors.New("add takes one flag at a time")
	case o.Verb != "add" && (o.On != nil || o.Description != nil || o.Paths != nil):
		return errors.New("--on, --off, --description and --path only go with add")
	case o.Verb != "ls" && o.JSON:
		return errors.New("--json only goes with ls")
	case o.SSH != "" && o.Dir != "":
		return errors.New("--ssh and --dir are exclusive")
	}
	for _, n := range o.Names {
		if bad := sites.InvalidFlagName(n); bad != "" {
			return fmt.Errorf("flag %q: %s", n, bad)
		}
	}
	for _, p := range o.Paths {
		if bad := sites.InvalidFlagPath(p); bad != "" {
			return fmt.Errorf("--path %q: %s", p, bad)
		}
	}
	return nil
}

// Run carries out o against the site's config in store.
func Run(o Options, store password.Store, w io.Writer) error {
	raw, _, err := store.Read(o.Host)
	if err != nil {
		return err
	}
	if o.Verb == "ls" {
		return list(w, o, raw)
	}
	out, changed := raw, false
	for _, name := range o.Names {
		var c bool
		switch o.Verb {
		case "on", "off":
			out, c, err = deploy.SetFlag(out, name, o.Verb == "on")
		case "add":
			out, c, err = deploy.DeclareFlag(out, name, deploy.FlagSpec{On: o.On, Description: o.Description, Paths: o.Paths})
		case "rm":
			out, c, err = deploy.RemoveFlag(out, name)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", o.Host, err)
		}
		changed = changed || c
	}
	if !changed {
		fmt.Fprintf(w, "✓ %s: nothing to change\n", o.Host)
		return list(w, o, out)
	}
	if err := store.Write(o.Host, out); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ %s: flags updated — live on zipgo's next reload\n", o.Host)
	return list(w, o, out)
}

// list prints a config's flags.
func list(w io.Writer, o Options, raw []byte) error {
	var cfg sites.Config
	if len(strings.TrimSpace(string(raw))) > 0 {
		var err error
		if cfg, err = sites.ParseConfig(raw); err != nil {
			return fmt.Errorf("%s: %s: %w", o.Host, sites.ConfigFileName, err)
		}
	}
	if o.JSON {
		flags := cfg.Flags
		if flags == nil {
			flags = map[string]sites.Flag{}
		}
		b, err := json.MarshalIndent(flags, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	}
	if !cfg.HasFlags() {
		fmt.Fprintf(w, "%s has no flags — zipgo flag add %s <name>\n", o.Host, o.Host)
		return nil
	}
	if len(cfg.Flags) == 0 {
		fmt.Fprintf(w, "%s: no flags (it still serves %s)\n", o.Host, sites.FlagsPath)
		return nil
	}
	width := 0
	for name := range cfg.Flags {
		width = max(width, len(name))
	}
	for _, name := range sites.FlagNames(cfg.Flags) {
		f := cfg.Flags[name]
		state := "⏸ off"
		if f.On {
			state = "● on "
		}
		line := fmt.Sprintf("  %s  %-*s", state, width, name)
		if f.Description != "" {
			line += "  " + f.Description
		}
		if len(f.Paths) > 0 {
			verb := "closes"
			if f.On {
				verb = "covers"
			}
			line += fmt.Sprintf("  (%s %s)", verb, strings.Join(f.Paths, ", "))
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	return nil
}
