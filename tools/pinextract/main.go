// SPDX-License-Identifier: Apache-2.0

// Command pinextract builds the board's pin table from the vendor's own
// documents.
//
// Three sources, each answering a different question:
//
//   - the core board schematic says which FPGA ball carries which signal
//     or which net of the board-to-board connector, and what Xilinx calls
//     that ball;
//   - the carrier board schematic says which board signal sits on each of
//     those nets;
//   - the user manual says what a signal is for, in words.
//
// The core board is read as text, from `pdftotext -layout`. Its pin
// table is two text lines per entry:
//
//	11   B15_L23_N   K16                       M15   B15_L24_P   11
//	     B15_L23_N   IO_L23N_T3_FWE_B_15             B15_L24_P
//
// The first line pairs a name with a ball, and the second pairs the same
// name with what Xilinx calls that ball.
//
// The carrier is read as word boxes, from `pdftotext -bbox-layout`,
// because its connector table cannot be read as lines. Each row of that
// table draws the connector's net above and the board's signal below,
// about three points apart, and the laid-out text puts a signal on the
// same line as the net of the row below it. Reading it that way gives
// LED1 the pin of LED2 and moves every Ethernet signal by one. With the
// boxes the rule is plain: a signal belongs to the net directly above it.
//
// The documents are the board vendor's and are not in this repository.
// The table this writes, data/pins.json, is.
package main

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A Pin is one ball of the FPGA package, with what the documents say
// about it.
type Pin struct {
	// The ball, as Xilinx names it: a letter and a number, K16.
	Pin string `json:"pin"`
	// The I/O bank it belongs to.
	Bank int `json:"bank,omitempty"`
	// What Xilinx calls the ball, IO_L23N_T3_FWE_B_15.
	Function string `json:"function,omitempty"`
	// The net of the board-to-board connector, for a ball that leaves
	// the core board: B15_L23_N.
	Net string `json:"net,omitempty"`
	// The board signal on the ball, PCIE_PERST.
	Signal string `json:"signal,omitempty"`
	// What that signal is for.
	Purpose string `json:"purpose,omitempty"`
	// Which part of the board it belongs to: pcie, hdmi, serial.
	Peripheral string `json:"peripheral,omitempty"`
	// Where the ball's signal is decided: the core board itself, or the
	// carrier through the connector.
	Board string `json:"board,omitempty"`
	// Other names the documents give the same ball. A transceiver ball
	// has a generic name on the core board and a functional one on the
	// carrier, and both are true.
	Aliases []string `json:"aliases,omitempty"`
	// Anything a reader has to know before trusting the row.
	Note string `json:"note,omitempty"`
	// Which documents the row was read from.
	Sources []string `json:"sources,omitempty"`
}

var (
	netRe    = regexp.MustCompile(`^B(\d+)_(?:L\d+_[PN]|IO\d+)$`)
	ballRe   = regexp.MustCompile(`^[A-Y]{1,2}\d{1,2}$`)
	funcRe   = regexp.MustCompile(`^IO_[A-Z0-9_]+?_(\d+)$`)
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+-]{2,}$`)
	numRe    = regexp.MustCompile(`^\d{1,3}$`)
	// A schematic writes a part beside the ball it decouples, so a
	// value or a designator can stand where a signal would.
	valueRe = regexp.MustCompile(
		`(?i)^\d+(\.\d+)?(nf|uf|pf|ma|ua|mm|ohm|k|r|v|mhz|khz)\d*$`)
	refRe = regexp.MustCompile(`^(?:[CRLUDQJPYFK]|RN|SW|TP)\d{1,4}$`)
	manualRe = regexp.MustCompile(
		`^\s*([A-Za-z][A-Za-z0-9_]{2,})\s+([A-Y]{1,2}\d{1,2})\s{2,}(\S.*?)\s*$`)
	// Words that stand beside a ball in these schematics and name no
	// signal: the supplies, the grounds and the sheet's own labels.
	notSignal = map[string]bool{
		"GND": true, "VCC": true, "VCCO": true, "VCCINT": true,
		"VCCAUX": true, "VCCBRAM": true, "VREF": true, "NC": true,
		"VCCADC": true, "GNDADC": true, "VCCAUX_IO": true,
	}
)

// peripherals maps the start of a signal's name to the part of the board
// it belongs to, and to words for what that part is. The words are used
// where the manual says nothing about the signal.
var peripherals = []struct {
	prefix     string
	peripheral string
	what       string
}{
	{"VCCO", "power", "a bank's supply"},
	{"VCCINT", "power", "the core supply"},
	{"VCCAUX", "power", "the auxiliary supply"},
	{"VCCBRAM", "power", "the block RAM supply"},
	{"VCCADC", "power", "the XADC supply"},
	{"VCCBATT", "power", "the battery input for the encryption key"},
	{"VREF", "power", "a bank's reference voltage"},
	{"MGTAV", "power", "a transceiver supply"},
	{"MGTREFCLK", "transceiver", "a transceiver reference clock"},
	{"CFGBVS", "config", "the configuration bank voltage select"},
	{"BOOT", "config", "a configuration mode input"},
	{"DXP", "thermal", "the temperature diode"},
	{"DXN", "thermal", "the temperature diode"},
	{"FPGA_T", "config", "the JTAG port"},
	{"TCK", "config", "the JTAG port"},
	{"TDO", "config", "the JTAG port"},
	{"TDI", "config", "the JTAG port"},
	{"TMS", "config", "the JTAG port"},
	{"9134_", "hdmi", "the SiI9134 HDMI encoder"},
	{"9013_", "lcd", "the LCD and camera header"},
	{"HDMI", "hdmi", "the SiI9134 HDMI encoder"},
	{"ETH_", "ethernet", "the JL2121 gigabit PHY, on RGMII"},
	{"PHY_", "ethernet", "the JL2121 gigabit PHY"},
	{"PCIE_", "pcie", "the PCIe slot, x4 in size and x2 in lanes"},
	{"MGT_", "transceiver", "a GTP transceiver: the PCIe slot or an SFP cage"},
	{"SFP", "sfp", "an SFP cage"},
	{"UART", "serial", "the CP2102N USB serial bridge"},
	{"LED", "led", "a user LED on the carrier, lit when driven low"},
	{"KEY", "button", "a user button on the carrier"},
	{"DDR3", "memory", "the DDR3 memory on the core board"},
	{"QSPI", "flash", "the N25Q128 configuration flash on the core board"},
	{"FLASH", "flash", "the N25Q128 configuration flash on the core board"},
	{"XADC", "xadc", "an analog input of the XADC"},
	{"SYS_CLK", "clock", "the board's 200 MHz differential clock"},
	{"CLK", "clock", "a clock"},
	{"AD9238", "adc", "the AD9238 converter header"},
	{"LCD", "lcd", "the LCD header"},
	{"CAM", "camera", "the camera header"},
	{"IO1_", "expansion", "the first expansion header"},
	{"IO2_", "expansion", "the second expansion header"},
	{"SD_", "sdcard", "the micro SD card socket"},
	{"FAN_", "fan", "the fan header"},
	{"RESET", "reset", "the board's reset button, low while pressed"},
	{"IIC", "i2c", "an I2C bus"},
	{"I2C", "i2c", "an I2C bus"},
	{"SI5338", "clock", "the Si5338 programmable clock"},
	{"USB", "usb", "the USB interface"},
	{"CCLK", "config", "the configuration clock"},
	{"INIT", "config", "a configuration line"},
	{"DONE", "config", "a configuration line"},
	{"PROG", "config", "a configuration line"},
}

// isPart says whether a word names a component rather than a signal: a
// value such as 470nF, or a designator such as C275.
func isPart(w string) bool {
	return valueRe.MatchString(w) || refRe.MatchString(w)
}

func classify(signal string) (string, string) {
	up := strings.ToUpper(signal)
	for _, p := range peripherals {
		if strings.HasPrefix(up, p.prefix) {
			return p.peripheral, p.what
		}
	}
	return "", ""
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		out = append(out, s.Text())
	}
	return out, s.Err()
}

// A word of a PDF, with where it sits on its page.
type word struct {
	page int
	text string
	x, y float64
}

// The XHTML that `pdftotext -bbox-layout` writes.
type bboxDoc struct {
	Pages []struct {
		Flows []struct {
			Blocks []struct {
				Lines []struct {
					Words []struct {
						XMin float64 `xml:"xMin,attr"`
						XMax float64 `xml:"xMax,attr"`
						YMin float64 `xml:"yMin,attr"`
						YMax float64 `xml:"yMax,attr"`
						Text string  `xml:",chardata"`
					} `xml:"word"`
				} `xml:"line"`
			} `xml:"block"`
		} `xml:"flow"`
	} `xml:"body>doc>page"`
}

// boxes reads the word boxes of a PDF.
func boxes(path string) ([]word, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc bboxDoc
	dec := xml.NewDecoder(strings.NewReader(string(blob)))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var out []word
	for pi, p := range doc.Pages {
		for _, fl := range p.Flows {
			for _, b := range fl.Blocks {
				for _, l := range b.Lines {
					for _, w := range l.Words {
						t := strings.TrimSpace(w.Text)
						if t == "" {
							continue
						}
						out = append(out, word{
							page: pi + 1,
							text: t,
							x:    (w.XMin + w.XMax) / 2,
							y:    (w.YMin + w.YMax) / 2,
						})
					}
				}
			}
		}
	}
	return out, nil
}

// entry is one row of a pin table: a name, which is either a connector
// net or a signal of the core board, and the ball that carries it.
type entry struct {
	name string
	ball string
	fn   string
}

// coreTable reads the core board's pin tables. Every ball named there is
// returned: those that leave the board as connector nets, and those that
// reach the memory, the flash, the clock or the configuration pins.
func coreTable(lines []string) map[string]*entry {
	byName := map[string]*entry{}
	for _, line := range lines {
		fields := strings.Fields(line)
		for i, f := range fields {
			if !ballRe.MatchString(f) {
				continue
			}
			for _, j := range []int{i - 1, i + 1} {
				if j < 0 || j >= len(fields) {
					continue
				}
				name := fields[j]
				if !nameRe.MatchString(name) || ballRe.MatchString(name) ||
					funcRe.MatchString(name) || isPart(name) ||
					notSignal[strings.ToUpper(name)] {
					continue
				}
				// A ball of the core board that no part of the board
				// claims is a supply, a pad or a stray label, and a row
				// for it says nothing.
				if !netRe.MatchString(name) {
					if kind, _ := classify(name); kind == "" {
						continue
					}
				}
				if e, seen := byName[name]; seen {
					if e.ball != f {
						fmt.Fprintf(os.Stderr, "%s: %s and %s\n",
							name, e.ball, f)
					}
					break
				}
				byName[name] = &entry{name: name, ball: f}
				break
			}
		}
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		for i, f := range fields {
			if !funcRe.MatchString(f) {
				continue
			}
			for _, j := range []int{i - 1, i + 1} {
				if j < 0 || j >= len(fields) {
					continue
				}
				if e, ok := byName[fields[j]]; ok && e.fn == "" {
					e.fn = f
					break
				}
			}
		}
	}
	return byName
}

// carrierSignals reads the carrier's connector tables and returns, per
// net, the board signal on it.
//
// A row of those tables draws the net and the signal as two labels, the
// net above and the signal about three points below it, both in the same
// column. So a signal belongs to the nearest net above it, within four
// points and within the width of a label to either side.
//
// Three facts check the rule, each from somewhere else. The manual says
// ETH_TXCK is P15, and the rule puts it there. The board's LED1 is the
// pin this project's own designs drive as their first LED, confirmed by
// looking at the board, and the rule agrees. The serial port works on
// hardware with UART1_TXD as the FPGA's output, and the rule puts that
// signal on the pin those designs drive.
func carrierSignals(ws []word) map[string]string {
	var nets, signals []word
	for _, w := range ws {
		switch {
		case netRe.MatchString(w.text):
			nets = append(nets, w)
		case numRe.MatchString(w.text) || ballRe.MatchString(w.text) ||
			funcRe.MatchString(w.text) || !nameRe.MatchString(w.text) ||
			isPart(w.text) || notSignal[strings.ToUpper(w.text)]:
			// Not a signal: a pin number, a ball, a supply, a value.
		default:
			signals = append(signals, w)
		}
	}
	// Every pairing a signal could have, nearest first, and each net
	// and each signal used once. A label is about forty points from its
	// net in the left tables and about sixty-five in the right ones, so
	// the window is wider than either and narrower than a column.
	type pairing struct {
		net, signal int
		dy, dx      float64
	}
	var all []pairing
	for si, sig := range signals {
		for ni, net := range nets {
			if net.page != sig.page {
				continue
			}
			dy := sig.y - net.y
			if dy < 1.5 || dy > 4.5 {
				continue
			}
			dx := sig.x - net.x
			if dx < -110 || dx > 110 {
				continue
			}
			if dx < 0 {
				dx = -dx
			}
			all = append(all, pairing{net: ni, signal: si, dy: dy, dx: dx})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].dy != all[j].dy {
			return all[i].dy < all[j].dy
		}
		return all[i].dx < all[j].dx
	})
	out := map[string]string{}
	usedSignal := map[int]bool{}
	for _, c := range all {
		if usedSignal[c.signal] {
			continue
		}
		if _, taken := out[nets[c.net].text]; taken {
			continue
		}
		out[nets[c.net].text] = signals[c.signal].text
		usedSignal[c.signal] = true
	}
	return out
}

// manualSays reads the user manual's tables, each row a signal, a ball
// and a description, and returns the description by signal name.
func manualSays(path string) (map[string]string, error) {
	says := map[string]string{}
	if path == "" {
		return says, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		m := manualRe.FindStringSubmatch(s.Text())
		if m == nil {
			continue
		}
		signal, text := m[1], strings.TrimSpace(m[3])
		if len(text) < 4 || ballRe.MatchString(text) || numRe.MatchString(text) {
			continue
		}
		if _, seen := says[signal]; !seen {
			says[signal] = text
		}
	}
	return says, s.Err()
}

// manualPins reads the manual's per-peripheral tables, which give a
// signal and the ball it reaches. Some tables put both on one line and
// some put the ball on the line after the signal, so both are read. The
// manual is the vendor's own statement of the mapping, so where it and a
// schematic disagree, this is the one to trust.
func manualPins(path string) (map[string]string, error) {
	pins := map[string]string{}
	if path == "" {
		return pins, nil
	}
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	pending := ""
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// A signal and a ball on one line, with nothing else between.
		for i, f := range fields {
			if !signalName(f) || i+1 >= len(fields) {
				continue
			}
			if ballRe.MatchString(fields[i+1]) {
				keep(pins, f, fields[i+1])
			}
		}
		// A ball alone on a line belongs to the signal seen last.
		if len(fields) == 1 && ballRe.MatchString(fields[0]) && pending != "" {
			keep(pins, pending, fields[0])
			pending = ""
			continue
		}
		if len(fields) == 1 && signalName(fields[0]) {
			pending = fields[0]
		}
	}
	return pins, nil
}

// signalName says whether a word is a board signal in the manual's
// tables. The tables write a bus as 9134_D[0], which is the same signal
// as the schematic's 9134_D0.
func signalName(w string) bool {
	w = strings.ReplaceAll(strings.ReplaceAll(w, "[", ""), "]", "")
	if !nameRe.MatchString(w) || ballRe.MatchString(w) || numRe.MatchString(w) {
		return false
	}
	peripheral, _ := classify(w)
	return peripheral != ""
}

// addName adds a name to a list, once.
func addName(list []string, name string) []string {
	for _, n := range list {
		if sameName(n, name) {
			return list
		}
	}
	return append(list, name)
}

// sameName says whether two spellings name the same signal. The
// documents differ in case: 9134_nRESET and 9134_NRESET.
func sameName(a, b string) bool {
	return strings.EqualFold(a, b)
}

func keep(pins map[string]string, signal, ball string) {
	signal = strings.ReplaceAll(strings.ReplaceAll(signal, "[", ""), "]", "")
	if _, seen := pins[signal]; !seen {
		pins[signal] = ball
	}
}

// An override is a fact the documents state badly or not at all, with
// the reason it is stated here instead.
type override struct {
	Pin        string `json:"pin"`
	Signal     string `json:"signal,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	Peripheral string `json:"peripheral,omitempty"`
	Note       string `json:"note"`
	Source     string `json:"source"`
}

func main() {
	coreFile := flag.String("core", "", "text of the core board schematic")
	carrierFile := flag.String("carrier", "", "word boxes of the carrier schematic")
	manualFile := flag.String("manual", "", "text of the user manual")
	overrideFile := flag.String("overrides", "", "facts the documents state badly")
	out := flag.String("out", "", "where the JSON goes; standard output if empty")
	flag.Parse()

	if *coreFile == "" || *carrierFile == "" {
		fmt.Fprintln(os.Stderr, "both -core and -carrier are needed")
		os.Exit(2)
	}
	coreLines, err := readLines(*coreFile)
	check(err)
	carrierWords, err := boxes(*carrierFile)
	check(err)
	says, err := manualSays(*manualFile)
	check(err)

	entries := coreTable(coreLines)
	signals := carrierSignals(carrierWords)
	fromManual, err := manualPins(*manualFile)
	check(err)

	byPin := map[string]*Pin{}
	for name, e := range entries {
		bank := 0
		if m := netRe.FindStringSubmatch(name); m != nil {
			bank, _ = strconv.Atoi(m[1])
		} else if m := funcRe.FindStringSubmatch(e.fn); m != nil {
			bank, _ = strconv.Atoi(m[1])
		}
		p := &Pin{
			Pin:      e.ball,
			Bank:     bank,
			Function: e.fn,
			Sources:  []string{"AC7A200 Core Board Schematic.pdf"},
		}
		if netRe.MatchString(name) {
			p.Net = name
			p.Board = "carrier"
			if signal, ok := signals[name]; ok {
				p.Signal = signal
				p.Sources = append(p.Sources,
					"AX7A200B Carrier Board Schematic.pdf")
			}
		} else {
			p.Signal = name
			p.Board = "core"
		}
		if p.Signal != "" {
			peripheral, what := classify(p.Signal)
			p.Peripheral = peripheral
			switch {
			case says[p.Signal] != "":
				p.Purpose = says[p.Signal]
				p.Sources = append(p.Sources, "AX7A200B User Manual REV1.0.pdf")
			case what != "":
				p.Purpose = what
			}
		}
		// Two names on one ball: the connector net wins, because the
		// carrier then says what the signal on it is.
		if old, clash := byPin[e.ball]; clash && old.Net != "" {
			continue
		}
		byPin[e.ball] = p
	}

	// The manual's own tables, which are the vendor saying which ball a
	// signal reaches. They fill what the schematics did not give, and a
	// disagreement is written into the row rather than hidden.
	for signal, ball := range fromManual {
		p, ok := byPin[ball]
		if !ok {
			p = &Pin{Pin: ball}
			byPin[ball] = p
		}
		if p.Signal != "" && !sameName(p.Signal, signal) {
			// A transceiver ball has a generic name, MGT_TX0_P, and a
			// functional one, SFP1_TX_P or PCIE_TX0_P. Both are true, and
			// the functional one is what a design writes, so it is the
			// name and the generic one is the alias.
			was, _ := classify(p.Signal)
			now, _ := classify(signal)
			if was == "transceiver" && now != "transceiver" {
				p.Aliases = addName(p.Aliases, p.Signal)
				p.Signal = signal
				p.Peripheral = now
				p.Sources = append(p.Sources, "AX7A200B User Manual REV1.0.pdf")
				continue
			}
			if now == "transceiver" && was != "transceiver" {
				p.Aliases = addName(p.Aliases, signal)
				p.Sources = append(p.Sources, "AX7A200B User Manual REV1.0.pdf")
				continue
			}
		}
		if p.Signal != "" && !sameName(p.Signal, signal) {
			// Two names for one ball. Where they belong to the same part
			// of the board, or to none, they are two names for one thing:
			// a transceiver ball is MGT_TX0_P on the core board and
			// SFP1_TX_P on the carrier. Where they belong to different
			// parts, the row says so and a reader decides.
			was, _ := classify(p.Signal)
			now, _ := classify(signal)
			p.Aliases = addName(p.Aliases, p.Signal)
			if was != now && was != "" && now != "" {
				p.Note = fmt.Sprintf(
					"the schematics call this ball %s, of the %s, and the "+
						"manual calls it %s, of the %s",
					p.Signal, was, signal, now)
			}
		}
		p.Signal = signal
		peripheral, what := classify(signal)
		if peripheral != "" {
			p.Peripheral = peripheral
		}
		if p.Purpose == "" {
			if says[signal] != "" {
				p.Purpose = says[signal]
			} else if what != "" {
				p.Purpose = what
			}
		}
		p.Sources = append(p.Sources, "AX7A200B User Manual REV1.0.pdf")
	}

	if *overrideFile != "" {
		blob, err := os.ReadFile(*overrideFile)
		check(err)
		var overrides []override
		check(json.Unmarshal(blob, &overrides))
		for _, o := range overrides {
			p, ok := byPin[o.Pin]
			if !ok {
				p = &Pin{Pin: o.Pin}
				byPin[o.Pin] = p
			}
			if o.Signal != "" {
				p.Signal = o.Signal
			}
			if o.Purpose != "" {
				p.Purpose = o.Purpose
			}
			if o.Peripheral != "" {
				p.Peripheral = o.Peripheral
			}
			p.Note = o.Note
			p.Sources = append(p.Sources, o.Source)
		}
	}

	list := make([]*Pin, 0, len(byPin))
	for _, p := range byPin {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Bank != list[j].Bank {
			return list[i].Bank < list[j].Bank
		}
		return ballLess(list[i].Pin, list[j].Pin)
	})

	blob, err := json.MarshalIndent(struct {
		Board  string `json:"board"`
		Part   string `json:"part"`
		Reader string `json:"reader"`
		Pins   []*Pin `json:"pins"`
	}{
		Board: "Alinx AX7A200B: a carrier board with an AC7A200 core board on it",
		Part:  "xc7a200tfbg484-2",
		Reader: "Every row states what the vendor's documents say. A row with " +
			"a note states what this repository worked out, and why.",
		Pins: list,
	}, "", "  ")
	check(err)
	blob = append(blob, '\n')
	if *out == "" {
		os.Stdout.Write(blob)
		return
	}
	check(os.WriteFile(*out, blob, 0o644))
	fmt.Fprintf(os.Stderr, "%d balls, %d of them with a signal\n",
		len(list), named(list))
}

func named(list []*Pin) int {
	n := 0
	for _, p := range list {
		if p.Signal != "" {
			n++
		}
	}
	return n
}

// ballLess orders package balls as a person reads them: by the letters
// first, then by the number.
func ballLess(a, b string) bool {
	al, an := split(a)
	bl, bn := split(b)
	if al != bl {
		if len(al) != len(bl) {
			return len(al) < len(bl)
		}
		return al < bl
	}
	return an < bn
}

func split(ball string) (string, int) {
	i := 0
	for i < len(ball) && (ball[i] < '0' || ball[i] > '9') {
		i++
	}
	n, _ := strconv.Atoi(ball[i:])
	return ball[:i], n
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
