// Copyright (c) 2018, The Gide Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gide

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goki/gi/giv"
	"github.com/goki/gi/oswin"
)

// ArgVars are variables that can be used for arguments to commands in CmdAndArgs
var ArgVars = []string{
	"{FilePath}",             // Current file name with full path.
	"{FileName}",             // Current file name only, without path.
	"{FileExt}",              // Extension of current file name.
	"{FileExtLC}",            // Extension of current file name, lowercase.
	"{FileNameNoExt}",        // Current file name without path and extension.
	"{FileDir}",              // Name only of current file's directory
	"{FileDirPath}",          // Full path to current file's directory.
	"{FileDirProjRel}",       // Path to current file's directory relative to project root
	"{ProjectDir}",           // Current project directory name, without full path.
	"{ProjectPath}",          // Full path to current project directory.
	"{CurLine}",              // Cursor current line number (starts at 1).
	"{CurCol}",               // Cursor current column number (starts at 0).
	"{SelStartLine}",         // Selection starting line (same as CurLine if no selection)
	"{SelStartCol}",          // Selection starting column (same as CurCol if no selection)
	"{SelEndLine}",           // Selection ending line (same as CurLine if no selection)
	"{SelEndCol}",            // Selection ending column (same as CurCol if no selection)
	"{CurSel}",               // Currently selected text.
	"{CurLineText}",          // Current line text under cursor.
	"{CurWord}",              // Current word under cursor.
	"{PromptFilePath}",       // Prompt user for a file, and this is the full path to that file
	"{PromptFileName}",       // Prompt user for a file, and this is the filename (only) of that file
	"{PromptFileDir}",        // Prompt user for a file, and this is the directory name (only) of that file
	"{PromptFileDirPath}",    // Prompt user for a file, and this is the full path to that directory
	"{PromptFileDirProjRel}", // Prompt user for a file, and this is the path of that directory relative to the project root.
	"{PromptString1}",        // Prompt user for a string -- this is it
	"{PromptString2}",        // Prompt user for another string -- this is it
}

// ArgVarVals are current values of arg var vals -- updated on demand when a
// command is invoked
var ArgVarVals map[string]string

// SetArgVarVals sets the current values for arg variables
func SetArgVarVals(avp *map[string]string, fpath, projpath string, tv *giv.TextView) {
	if *avp == nil {
		*avp = make(map[string]string, len(ArgVars))
	}
	av := *avp

	fpath = filepath.Clean(fpath)
	projpath = filepath.Clean(projpath)

	dirpath, fnm := filepath.Split(fpath)
	dirpath = filepath.Clean(dirpath)
	_, dir := filepath.Split(dirpath)
	dirrel, _ := filepath.Rel(projpath, dirpath)

	_, projdir := filepath.Split(projpath)

	ext := filepath.Ext(fnm)
	extlc := strings.ToLower(ext)
	fnmnoext := strings.TrimSuffix(fnm, ext)

	av["{FilePath}"] = fpath
	av["{FileName}"] = fnm
	av["{FileExt}"] = ext
	av["{FileExtLC}"] = extlc
	av["{FileNameNoExt}"] = fnmnoext
	av["{FileDir}"] = dir
	av["{FileDirPath}"] = dirpath
	av["{FileDirProjRel}"] = dirrel
	av["{ProjectDir}"] = projdir
	av["{ProjectPath}"] = projpath
	if tv != nil {
		av["{CurLine}"] = fmt.Sprintf("%v", tv.CursorPos.Ln)
		av["{CurCol}"] = fmt.Sprintf("%v", tv.CursorPos.Ch)             // not quite col
		av["{SelStartLine}"] = fmt.Sprintf("%v", tv.SelectReg.Start.Ln) // check for no sel
		av["{SelStartCol}"] = fmt.Sprintf("%v", tv.SelectReg.Start.Ch)
		av["{SelEndLine}"] = fmt.Sprintf("%v", tv.SelectReg.End.Ln)  // check for no sel
		av["{SelEndCol}"] = fmt.Sprintf("%v", tv.SelectReg.Start.Ch) // check for no sel
		av["{CurSel}"] = ""                                          // todo get sel
		av["{CurLineText}"] = ""                                     // todo get cur line
		av["{CurWord}"] = ""                                         // todo get word
	} else {
		av["{CurLine}"] = ""
		av["{CurCol}"] = ""
		av["{SelStartLine}"] = ""
		av["{SelStartCol}"] = ""
		av["{SelEndLine}"] = ""
		av["{SelEndCol}"] = ""
		av["{CurSel}"] = ""
		av["{CurLineText}"] = ""
		av["{CurWord}"] = ""
	}
}

// BindArgVars replaces the variables in the given arg string with their values
func BindArgVars(arg string) string {
	sz := len(arg)
	bs := []byte(arg)
	ci := 0
	gotquote := false
	for ci < sz {
		sb := bytes.Index(bs[ci:], []byte("{"))
		if sb < 0 {
			break
		}
		ci += sb
		if ci-1 >= 0 && bs[ci-1] == '\\' { // quoted
			ci++
			gotquote = true
			continue
		}
		eb := bytes.Index(bs[ci+1:], []byte("}"))
		if eb < 0 {
			break
		}
		eb += ci + 1
		vnm := string(bs[ci : eb+1])
		// fmt.Printf("%v\n", vnm)
		if strings.HasPrefix(vnm, "{Prompt") {
			// todo: do prompts!
		} else {
			if val, ok := ArgVarVals[vnm]; ok {
				end := make([]byte, sz-(eb+1))
				copy(end, bs[eb+1:])
				bs = append(bs[:ci], []byte(val)...)
				ci = len(bs)
				bs = append(bs, end...)
			}
			sz = len(bs)
		}
	}
	if gotquote {
		bs = bytes.Replace(bs, []byte("\\{"), []byte("{"), -1)
	}
	// note: need to quote this out for testing for the time being..
	if oswin.TheApp.Platform() == oswin.Windows {
		bs = bytes.Replace(bs, []byte("}/{"), []byte("}\\{"), -1)
	}
	return string(bs)
}