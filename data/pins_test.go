// SPDX-License-Identifier: Apache-2.0

// Tests of the pin table itself. The table is data rather than code, so
// these are the only thing standing between a wrong reading of a
// schematic and a design that does not work on the board.
package data_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

type pin struct {
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
	Board string `json:"board"`
	Part  string `json:"part"`
	Pins  []pin  `json:"pins"`
}

func load(t *testing.T) table {
	t.Helper()
	blob, err := os.ReadFile("data/pins.json")
	if err != nil {
		blob, err = os.ReadFile("pins.json")
	}
	if err != nil {
		t.Fatalf("the pin table: %v", err)
	}
	var out table
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("the pin table: %v", err)
	}
	return out
}

func byPin(t *testing.T) map[string]pin {
	out := map[string]pin{}
	for _, p := range load(t).Pins {
		if _, twice := out[p.Pin]; twice {
			t.Errorf("%s: two rows for one ball", p.Pin)
		}
		out[p.Pin] = p
	}
	return out
}

// The three facts that check how the carrier's tables are read. Each
// comes from somewhere other than those tables: the vendor's manual, a
// board that was looked at, and a board that talks to its host.
func TestTheReadingOfTheCarrierIsRight(t *testing.T) {
	pins := byPin(t)
	for _, want := range []struct {
		ball, signal, why string
	}{
		{"P15", "ETH_TXCK", "the manual's own table says so"},
		{"L13", "LED1", "the designs in HDL/txhdl drive it, confirmed by eye"},
		{"M13", "LED2", "the designs in HDL/txhdl drive it, confirmed by eye"},
		{"K14", "LED3", "the designs in HDL/txhdl drive it, confirmed by eye"},
		{"K13", "LED4", "the designs in HDL/txhdl drive it, confirmed by eye"},
		{"L15", "UART1_TXD", "a board that talks to its host drives it"},
		{"L14", "UART1_RXD", "the other half of the same port"},
	} {
		if got := pins[want.ball].Signal; got != want.signal {
			t.Errorf("%s: the table says %q and it is %q, because %s",
				want.ball, got, want.signal, want.why)
		}
	}
}

// The PCIe reset, which no table of the manual states and which gates
// every PCIe design on this board.
func TestThePcieResetIsWhereTheSchematicsPutIt(t *testing.T) {
	pins := byPin(t)
	p := pins["L16"]
	if p.Signal != "PCIE_PERST" {
		t.Fatalf("L16 carries %q, and the schematics put PCIE_PERST there", p.Signal)
	}
	if p.Bank != 15 {
		t.Errorf("L16 is in bank %d, and bank 15 is the 3.3 V bank", p.Bank)
	}
	if !strings.Contains(p.Function, "L23P") {
		t.Errorf("L16 is %q, and the net is B15_L23_P", p.Function)
	}
}

// A ball's Xilinx name states its bank and its differential pair, and
// its net states the same two things. They have to agree, or one of the
// two was read from the wrong row.
func TestEveryNetAgreesWithTheBallItIsOn(t *testing.T) {
	netRe := regexp.MustCompile(`^B(\d+)_L(\d+)_([PN])$`)
	for _, p := range load(t).Pins {
		m := netRe.FindStringSubmatch(p.Net)
		if m == nil || p.Function == "" {
			continue
		}
		want := "_L" + m[2] + m[3] + "_"
		if !strings.Contains(p.Function, want) {
			t.Errorf("%s: the net %s and the name %s disagree",
				p.Pin, p.Net, p.Function)
		}
		if !strings.HasSuffix(p.Function, "_"+m[1]) {
			t.Errorf("%s: the net %s says bank %s and the name is %s",
				p.Pin, p.Net, m[1], p.Function)
		}
	}
}

// Every named ball says what its signal is for, and which part of the
// board it belongs to. A row without those is a row nobody can use.
func TestEveryNamedBallSaysWhatItIsFor(t *testing.T) {
	for _, p := range load(t).Pins {
		if p.Signal == "" {
			continue
		}
		if p.Purpose == "" {
			t.Errorf("%s, %s: no purpose", p.Pin, p.Signal)
		}
		if p.Peripheral == "" {
			t.Errorf("%s, %s: no peripheral", p.Pin, p.Signal)
		}
		if len(p.Sources) == 0 {
			t.Errorf("%s, %s: no source", p.Pin, p.Signal)
		}
	}
}

// The table covers the board rather than a corner of it. These counts
// are floors and not exact numbers, so that the tests do not have to be
// edited every time a reading improves.
func TestTheTableCoversTheBoard(t *testing.T) {
	all := load(t)
	if all.Part != "xc7a200tfbg484-2" {
		t.Errorf("the part is %q", all.Part)
	}
	named := 0
	kinds := map[string]int{}
	for _, p := range all.Pins {
		if p.Signal != "" {
			named++
			kinds[p.Peripheral]++
		}
	}
	if len(all.Pins) < 300 {
		t.Errorf("%d balls, and the package has 484", len(all.Pins))
	}
	if named < 280 {
		t.Errorf("%d named balls", named)
	}
	for _, want := range []struct {
		kind  string
		least int
	}{
		{"memory", 60}, {"ethernet", 12}, {"hdmi", 20},
		{"pcie", 8}, {"led", 4}, {"button", 4}, {"serial", 2},
	} {
		if kinds[want.kind] < want.least {
			t.Errorf("%s: %d balls, and the board has at least %d",
				want.kind, kinds[want.kind], want.least)
		}
	}
}
