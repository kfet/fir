// binsize prints per-package function text sizes from a Go binary.
//
// Usage: go run docs/binsize.go <binary>
//
// Parses the Go symbol table (gopclntab) to get accurate function sizes,
// grouped by top-level package. Works on both stripped and unstripped binaries.
package main

import (
	"debug/gosym"
	"debug/macho"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <binary>\n", os.Args[0])
		os.Exit(1)
	}

	f, err := macho.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	var textAddr uint64
	var pclnData []byte
	for _, s := range f.Sections {
		if s.Name == "__text" {
			textAddr = s.Addr
		}
		if s.Name == "__gopclntab" {
			pclnData, _ = s.Data()
		}
	}
	if pclnData == nil {
		fmt.Fprintln(os.Stderr, "no __gopclntab section found")
		os.Exit(1)
	}

	table, err := gosym.NewTable(nil, gosym.NewLineTable(pclnData, textAddr))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	pkgSizes := map[string]int64{}
	for _, fn := range table.Funcs {
		pkg := fn.PackageName()
		if pkg == "" {
			continue
		}
		// Group by module: github.com/org/repo for external, top-level for stdlib
		parts := strings.Split(pkg, "/")
		if len(parts) >= 3 && strings.Contains(parts[0], ".") {
			pkg = parts[0] + "/" + parts[1] + "/" + parts[2]
		} else if len(parts) >= 2 {
			pkg = parts[0] + "/" + parts[1]
		}
		pkgSizes[pkg] += int64(fn.End - fn.Entry)
	}

	type entry struct {
		pkg  string
		size int64
	}
	var entries []entry
	var total int64
	for p, s := range pkgSizes {
		entries = append(entries, entry{p, s})
		total += s
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size > entries[j].size })

	fi, err := os.Stat(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binSize := fi.Size()

	fmt.Printf("Binary size:         %d KB (%.1f MB)\n", binSize/1024, float64(binSize)/1048576)
	fmt.Printf("Function text:       %d KB (%.1f MB)\n", total/1024, float64(total)/1048576)
	fmt.Printf("Metadata overhead:   %d KB (%.1f MB) — pclntab, rodata, type descriptors; scales with code\n\n",
		(binSize-total)/1024, float64(binSize-total)/1048576)
	for i, e := range entries {
		if i > 29 {
			break
		}
		fmt.Printf("%7d KB  (%4.1f%%)  %s\n", e.size/1024, float64(e.size)*100/float64(total), e.pkg)
	}
}
