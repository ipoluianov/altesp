package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
)

// cmdEFuse reads the eFuses, or with "burn" plans and burns changes:
// altesp efuse [--port P] [--json] [--all]
// altesp efuse burn [--port P] FIELD=VALUE... [--confirm BURN]
// Without --confirm the plan is only shown.
func cmdEFuse(args []string, e env) int {
	burn := len(args) > 0 && args[0] == "burn"
	if burn {
		args = args[1:]
	}
	fs := flag.NewFlagSet("efuse", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o opts
	var confirm string
	var all bool
	fs.StringVar(&o.port, "port", "", "")
	fs.StringVar(&o.port, "p", "", "")
	fs.BoolVar(&o.json, "json", false, "")
	fs.BoolVar(&o.noStub, "no-stub", burn, "")
	fs.StringVar(&confirm, "confirm", "", "")
	fs.BoolVar(&all, "all", false, "")
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return usageError(e, err)
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	var changes []ops.EFuseChange
	for _, a := range rest {
		name, value, ok := strings.Cut(a, "=")
		if !burn || !ok {
			return usageError(e, fmt.Errorf("efuse: unexpected %q", a))
		}
		v, err := ops.ParseNumber(value)
		if err != nil {
			return fail(e, err)
		}
		changes = append(changes, ops.EFuseUint(name, uint64(v)))
	}
	if burn && len(changes) == 0 {
		return usageError(e, errors.New("efuse burn: give FIELD=VALUE"))
	}
	ctx, stop := interrupted()
	defer stop()
	s, err := connect(ctx, o, e)
	if err != nil {
		return fail(e, err)
	}
	defer s.Close(context.Background(), true)
	d, err := s.ReadEFuse(ctx)
	if err != nil {
		return fail(e, err)
	}
	if !burn {
		printEFuse(e, o, d, all)
		return exitOK
	}
	p, err := s.PlanEFuse(d, changes)
	if err != nil {
		return fail(e, err)
	}
	fmt.Fprintln(e.stdout, "Changes:")
	for _, c := range p.Changes {
		fmt.Fprintln(e.stdout, "  "+c)
	}
	for _, w := range p.Warnings {
		fmt.Fprintln(e.stdout, "Warning: "+w)
	}
	if confirm != ops.BurnWord {
		fmt.Fprintf(e.stdout, "\nNothing burned. eFuses can't be changed back: repeat with --confirm %s to burn.\n", ops.BurnWord)
		return exitOK
	}
	res, err := s.BurnEFuse(ctx, p, confirm)
	if res != nil {
		for _, n := range res.Notes {
			fmt.Fprintln(e.stdout, n)
		}
	}
	if err != nil {
		return fail(e, err)
	}
	fmt.Fprintln(e.stdout, "Burned")
	return exitOK
}

func printEFuse(e env, o opts, d *ops.EFuseDump, all bool) {
	type row struct {
		Name, Category, Value string
		WriteProtected        bool `json:",omitempty"`
		ReadProtected         bool `json:",omitempty"`
	}
	var rows []row
	for _, v := range d.Values {
		if !all && v.Uint() == 0 && !v.WriteProtected && !v.ReadProtected {
			continue
		}
		rows = append(rows, row{v.Field.Name, v.Field.Category, v.Text, v.WriteProtected, v.ReadProtected})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Category < rows[j].Category })
	result(e, o, map[string]any{"coding": d.CodingScheme, "fields": rows, "errors": d.Errors}, func() {
		fmt.Fprintf(e.stdout, "Coding: %s\n", d.CodingScheme)
		cat := ""
		for _, r := range rows {
			if r.Category != cat {
				cat = r.Category
				fmt.Fprintf(e.stdout, "\n%s:\n", cat)
			}
			mark := ""
			if r.WriteProtected {
				mark += " [write-protected]"
			}
			if r.ReadProtected {
				mark += " [read-protected]"
			}
			fmt.Fprintf(e.stdout, "  %-32s %s%s\n", r.Name, r.Value, mark)
		}
		if !all {
			fmt.Fprintln(e.stdout, "\n(fields at 0 are left out; --all shows them)")
		}
		for _, er := range d.Errors {
			fmt.Fprintln(e.stdout, "Error: "+er)
		}
	})
}
