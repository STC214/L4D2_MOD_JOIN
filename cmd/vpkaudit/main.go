package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	vpkSignature    = 0x55aa1234
	inlineArchive   = 0x7fff
	entryTerminator = 0xffff
)

type Entry struct {
	Path         string `json:"path"`
	CRC          uint32 `json:"crc"`
	ArchiveIndex uint16 `json:"archive_index"`
	Offset       uint32 `json:"offset"`
	Length       uint32 `json:"length"`
	Preload      []byte `json:"-"`
}

type Package struct {
	Name       string         `json:"name"`
	Size       int64          `json:"size"`
	FileCount  int            `json:"file_count"`
	Extensions map[string]int `json:"extensions"`
	TopDirs    map[string]int `json:"top_dirs"`
	AddonInfo  string         `json:"addon_info,omitempty"`
	Entries    []Entry        `json:"entries"`
}

type Conflict struct {
	Path     string   `json:"path"`
	Packages []string `json:"packages"`
	SameCRC  bool     `json:"same_crc"`
}

type Report struct {
	Packages  []Package  `json:"packages"`
	Conflicts []Conflict `json:"conflicts"`
}

func readCString(r *bufio.Reader) (string, error) {
	s, err := r.ReadString(0)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(s, "\x00"), nil
}

func readPackage(path string) (Package, error) {
	f, err := os.Open(path)
	if err != nil {
		return Package{}, err
	}
	defer f.Close()

	var signature, version, treeSize uint32
	for _, dst := range []any{&signature, &version, &treeSize} {
		if err := binary.Read(f, binary.LittleEndian, dst); err != nil {
			return Package{}, err
		}
	}
	if signature != vpkSignature {
		return Package{}, fmt.Errorf("invalid VPK signature: %08x", signature)
	}
	if version != 1 {
		return Package{}, fmt.Errorf("unsupported VPK version: %d", version)
	}

	tree := make([]byte, treeSize)
	if _, err := io.ReadFull(f, tree); err != nil {
		return Package{}, err
	}
	r := bufio.NewReader(strings.NewReader(string(tree)))
	pkg := Package{
		Name:       filepath.Base(path),
		Extensions: map[string]int{},
		TopDirs:    map[string]int{},
	}
	if info, err := f.Stat(); err == nil {
		pkg.Size = info.Size()
	}

	for {
		ext, err := readCString(r)
		if err != nil {
			return Package{}, fmt.Errorf("read extension: %w", err)
		}
		if ext == "" {
			break
		}
		for {
			dir, err := readCString(r)
			if err != nil {
				return Package{}, fmt.Errorf("read directory: %w", err)
			}
			if dir == "" {
				break
			}
			for {
				name, err := readCString(r)
				if err != nil {
					return Package{}, fmt.Errorf("read filename: %w", err)
				}
				if name == "" {
					break
				}
				var crc uint32
				var preloadBytes, archiveIndex uint16
				var offset, length uint32
				var terminator uint16
				for _, dst := range []any{&crc, &preloadBytes, &archiveIndex, &offset, &length, &terminator} {
					if err := binary.Read(r, binary.LittleEndian, dst); err != nil {
						return Package{}, fmt.Errorf("read entry %s: %w", name, err)
					}
				}
				if terminator != entryTerminator {
					return Package{}, fmt.Errorf("invalid entry terminator for %s: %04x", name, terminator)
				}
				preload := make([]byte, preloadBytes)
				if _, err := io.ReadFull(r, preload); err != nil {
					return Package{}, fmt.Errorf("read preload for %s: %w", name, err)
				}
				cleanDir := strings.Trim(dir, "/\\ ")
				fullName := name
				if ext != " " {
					fullName += "." + ext
				}
				fullPath := fullName
				if cleanDir != "" {
					fullPath = cleanDir + "/" + fullName
				}
				fullPath = strings.ToLower(strings.ReplaceAll(fullPath, "\\", "/"))
				pkg.Entries = append(pkg.Entries, Entry{
					Path:         fullPath,
					CRC:          crc,
					ArchiveIndex: archiveIndex,
					Offset:       offset,
					Length:       length,
					Preload:      preload,
				})
				pkg.Extensions[strings.ToLower(ext)]++
				top := strings.SplitN(fullPath, "/", 2)[0]
				pkg.TopDirs[top]++
			}
		}
	}
	pkg.FileCount = len(pkg.Entries)

	for _, entry := range pkg.Entries {
		if entry.Path != "addoninfo.txt" {
			continue
		}
		content, err := readEntry(f, int64(12+treeSize), entry)
		if err == nil {
			pkg.AddonInfo = string(content)
		}
		break
	}
	return pkg, nil
}

func readEntry(f *os.File, dataStart int64, entry Entry) ([]byte, error) {
	if entry.ArchiveIndex != inlineArchive {
		return nil, errors.New("external archive entry")
	}
	content := make([]byte, 0, len(entry.Preload)+int(entry.Length))
	content = append(content, entry.Preload...)
	if entry.Length == 0 {
		return content, nil
	}
	buf := make([]byte, entry.Length)
	if _, err := f.ReadAt(buf, dataStart+int64(entry.Offset)); err != nil {
		return nil, err
	}
	return append(content, buf...), nil
}

func main() {
	dir := "workshop"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.vpk"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Strings(files)

	report := Report{}
	owners := map[string][]struct {
		name string
		crc  uint32
	}{}
	for _, path := range files {
		pkg, err := readPackage(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		for _, entry := range pkg.Entries {
			owners[entry.Path] = append(owners[entry.Path], struct {
				name string
				crc  uint32
			}{pkg.Name, entry.CRC})
		}
		report.Packages = append(report.Packages, pkg)
	}
	for path, entries := range owners {
		if len(entries) < 2 {
			continue
		}
		conflict := Conflict{Path: path, SameCRC: true}
		for i, entry := range entries {
			conflict.Packages = append(conflict.Packages, entry.name)
			if i > 0 && entry.crc != entries[0].crc {
				conflict.SameCRC = false
			}
		}
		sort.Strings(conflict.Packages)
		report.Conflicts = append(report.Conflicts, conflict)
	}
	sort.Slice(report.Conflicts, func(i, j int) bool {
		return report.Conflicts[i].Path < report.Conflicts[j].Path
	})

	out := io.Writer(os.Stdout)
	var outputFile *os.File
	if len(os.Args) > 2 {
		outputFile, err = os.Create(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer outputFile.Close()
		out = outputFile
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
