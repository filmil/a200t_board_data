// SPDX-License-Identifier: Apache-2.0

// Command pintex writes the document's tables from the pin table.
//
// Nothing in the document is typed by hand from the data: the tables are
// generated at build time from data/pins.json, so a document that builds
// cannot disagree with the data it describes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Pin struct {
	Pin        string   `json:"pin"`
	Bank       int      `json:"bank"`
	Function   string   `json:"function"`
	Net        string   `json:"net"`
	Signal     string   `json:"signal"`
	Purpose    string   `json:"purpose"`
	Peripheral string   `json:"peripheral"`
	Board      string   `json:"board"`
	Aliases    []string `json:"aliases"`
	Note       string   `json:"note"`
	Sources    []string `json:"sources"`
}

type table struct {
	Board  string `json:"board"`
	Part   string `json:"part"`
	Reader string `json:"reader"`
	Pins   []Pin  `json:"pins"`
}

// The parts of the board, in the order a reader wants them: what a
// design drives first, then the headers, then what the board does for
// itself.
var order = []struct{ key, title string }{
	{"serial", "The serial port"},
	{"led", "The LEDs"},
	{"button", "The buttons"},
	{"clock", "The clocks"},
	{"memory", "The DDR3 memory"},
	{"flash", "The configuration flash"},
	{"ethernet", "The gigabit Ethernet PHY"},
	{"hdmi", "The HDMI encoder"},
	{"pcie", "The PCIe slot"},
	{"sfp", "The SFP cages"},
	{"transceiver", "The remaining transceiver pins"},
	{"i2c", "The I2C bus"},
	{"xadc", "The analog inputs"},
	{"config", "The configuration pins"},
	{"lcd", "The LCD and camera header"},
	{"expansion", "The expansion headers"},
}

func esc(s string) string {
	r := strings.NewReplacer(
		`\`, `\textbackslash{}`,
		"_", `\_`,
		"%", `\%`,
		"&", `\&`,
		"#", `\#`,
		"$", `\$`,
		"{", `\{`,
		"}", `\}`,
		"~", `\textasciitilde{}`,
		"^", `\textasciicircum{}`,
	)
	return r.Replace(s)
}

func main() {
	in := flag.String("in", "", "the pin table, as JSON")
	out := flag.String("out", "", "where the tables go")
	countsOut := flag.String("counts", "", "where the counting macros go")
	flag.Parse()
	blob, err := os.ReadFile(*in)
	check(err)
	var t table
	check(json.Unmarshal(blob, &t))

	var b, counts strings.Builder
	for _, w := range []*strings.Builder{&b, &counts} {
		fmt.Fprintf(w, "%% Written by //tools/pintex from data/pins.json.\n")
		fmt.Fprintf(w, "%% Do not edit: edit the data, or the tool.\n\n")
	}

	// What the board is, in numbers.
	named, banks := 0, map[int]int{}
	byPeripheral := map[string][]Pin{}
	for _, p := range t.Pins {
		if p.Signal != "" {
			named++
		}
		if p.Bank != 0 {
			banks[p.Bank]++
		}
		if p.Peripheral != "" {
			byPeripheral[p.Peripheral] = append(byPeripheral[p.Peripheral], p)
		}
	}
	fmt.Fprintf(&counts, "\\newcommand{\\ballcount}{%d}\n", len(t.Pins))
	fmt.Fprintf(&counts, "\\newcommand{\\namedcount}{%d}\n", named)
	fmt.Fprintf(&counts, "\\newcommand{\\bankcount}{%d}\n", len(banks))

	// The banks, and what each of them carries.
	var bankKeys []int
	for k := range banks {
		bankKeys = append(bankKeys, k)
	}
	sort.Ints(bankKeys)
	fmt.Fprintf(&b, "\\begin{longtable}{@{}rrl@{}}\n")
	fmt.Fprintf(&b, "\\caption{The I/O banks, and what reaches each of them.}\\\\\n")
	fmt.Fprintf(&b, "\\toprule\nBank & Balls & What is on it \\\\\n\\midrule\n\\endfirsthead\n")
	fmt.Fprintf(&b, "\\toprule\nBank & Balls & What is on it \\\\\n\\midrule\n\\endhead\n")
	for _, k := range bankKeys {
		kinds := map[string]bool{}
		for _, p := range t.Pins {
			if p.Bank == k && p.Peripheral != "" {
				kinds[p.Peripheral] = true
			}
		}
		var list []string
		for kind := range kinds {
			list = append(list, kind)
		}
		sort.Strings(list)
		fmt.Fprintf(&b, "%d & %d & %s \\\\\n", k, banks[k], esc(strings.Join(list, ", ")))
	}
	fmt.Fprintf(&b, "\\bottomrule\n\\end{longtable}\n\n")

	// A table per part of the board.
	for _, o := range order {
		pins := byPeripheral[o.key]
		if len(pins) == 0 {
			continue
		}
		sort.Slice(pins, func(i, j int) bool { return pins[i].Signal < pins[j].Signal })
		fmt.Fprintf(&b, "\\subsection{%s}\n", esc(o.title))
		fmt.Fprintf(&b, "\\begin{longtable}{@{}lllp{6.2cm}@{}}\n")
		fmt.Fprintf(&b, "\\caption{%s: %d balls.}\\\\\n", esc(o.title), len(pins))
		head := "\\toprule\nSignal & Ball & Bank & What it is \\\\\n\\midrule\n"
		fmt.Fprintf(&b, "%s\\endfirsthead\n%s\\endhead\n", head, head)
		for _, p := range pins {
			purpose := p.Purpose
			if len(p.Aliases) > 0 {
				purpose += ", also called " + strings.Join(p.Aliases, ", ")
			}
			bank := fmt.Sprintf("%d", p.Bank)
			if p.Bank == 0 {
				bank = "--"
			}
			fmt.Fprintf(&b, "\\texttt{%s} & \\texttt{%s} & %s & %s \\\\\n",
				esc(p.Signal), esc(p.Pin), bank, esc(purpose))
		}
		fmt.Fprintf(&b, "\\bottomrule\n\\end{longtable}\n\n")
	}

	// Every row that carries a note: the places where the documents do
	// not agree, or where this repository worked something out.
	var notes []Pin
	for _, p := range t.Pins {
		if p.Note != "" {
			notes = append(notes, p)
		}
	}
	fmt.Fprintf(&counts, "\\newcommand{\\notecount}{%d}\n", len(notes))
	if len(notes) > 0 {
		fmt.Fprintf(&b, "\\begin{longtable}{@{}llp{9cm}@{}}\n")
		fmt.Fprintf(&b, "\\caption{Where the documents disagree, or where this "+
			"repository decided something.}\\\\\n")
		head := "\\toprule\nBall & Signal & What to know \\\\\n\\midrule\n"
		fmt.Fprintf(&b, "%s\\endfirsthead\n%s\\endhead\n", head, head)
		for _, p := range notes {
			fmt.Fprintf(&b, "\\texttt{%s} & \\texttt{%s} & %s \\\\\n",
				esc(p.Pin), esc(p.Signal), esc(p.Note))
		}
		fmt.Fprintf(&b, "\\bottomrule\n\\end{longtable}\n\n")
	}

	check(os.WriteFile(*out, []byte(b.String()), 0o644))
	if *countsOut != "" {
		check(os.WriteFile(*countsOut, []byte(counts.String()), 0o644))
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
