# a200t_board_data

[![ci](https://github.com/filmil/a200t_board_data/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/filmil/a200t_board_data/actions/workflows/ci.yml)
[![release](https://github.com/filmil/a200t_board_data/actions/workflows/release.yml/badge.svg)](https://github.com/filmil/a200t_board_data/releases)

What the Alinx AX7A200B is, extracted from the vendor's own documents and
kept in a form a person or a program can query.

The board is an `xc7a200tfbg484-2` on an AC7A200 core board, sitting on a
carrier board.
Its documents say what reaches which pin, in three places and three
formats, and none of them is a table anybody can query.
This repository turns them into one.

## What is here

* `data/pins.json` is every ball of the package, with its bank, what
  Xilinx calls it, the connector net it reaches, the board signal on it,
  what that signal is for, and which documents say so.
* `doc/a200t.tex` builds a PDF that describes the board, with every
  table generated from that JSON at build time.
* `tools/pinextract` reads the vendor's PDFs and writes the JSON.
* `tools/pintex` writes the document's tables from the JSON.
* `data/pins_test.go` checks the data against facts established
  elsewhere, including hardware.

## Building

Nothing has to be installed beyond `bazelisk`.
LaTeX and Go come from the build.

```sh
bazel build //doc:a200t     # -> bazel-bin/doc/a200t.pdf
bazel test //...            # the checks on the data
```

## Asking the board a question

```sh
# Which ball is the PCIe reset on?
jq -r '.pins[] | select(.signal == "PCIE_PERST") | .pin' data/pins.json

# What is on bank 15?
jq -r '.pins[] | select(.bank == 15) | "\(.pin) \(.signal // "-")"' data/pins.json

# Which balls does the HDMI encoder use?
jq -r '.pins[] | select(.peripheral == "hdmi") | "\(.signal) \(.pin)"' data/pins.json
```

## Regenerating the data

The vendor's PDFs are not in this repository.
They come with the board, under
`04_Schematic_PCB_Form Factors/Schematic/` and beside the user manual.
With `pdftotext` from poppler:

```sh
pdftotext -layout "AC7A200 Core Board Schematic.pdf" core.txt
pdftotext -bbox-layout "AX7A200B Carrier Board Schematic.pdf" carrier.xml
pdftotext -layout "AX7A200B User Manual REV1.0.pdf" manual.txt

bazel run //tools/pinextract -- \
    -core "$PWD/core.txt" -carrier "$PWD/carrier.xml" \
    -manual "$PWD/manual.txt" -out "$PWD/data/pins.json"
bazel test //...
```

The carrier is read as word boxes rather than as text on purpose.
Each row of its connector tables draws the net label above the signal
label, so a line-by-line reading pairs every signal with the wrong net.
`doc/a200t.tex` says how the rule was checked, and `data/pins_test.go`
asserts the three facts that check it.

## Releases

A release is cut when the data or the document changes, and never for a
run that changes nothing.
Each release carries `a200t.pdf`, `pins.json` and `checksums.txt`.

## Licence

Apache-2.0, for what this repository writes.
The board's documents belong to Alinx and are not distributed here; the
data extracted from them is stated as fact, with its source named per
row.
