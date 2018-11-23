// Copyright (c) 2018, The Gide Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
package gide provides the core Gide editor object.

Derived classes can extend the functionality for specific domains.

*/
package gide

import (
	"bytes"
	"fmt"
	"html"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/goki/gi/filecat"
	"github.com/goki/gi/gi"
	"github.com/goki/gi/giv"
	"github.com/goki/gi/histyle"
	"github.com/goki/gi/oswin"
	"github.com/goki/gi/oswin/key"
	"github.com/goki/gi/units"
	"github.com/goki/ki"
	"github.com/goki/ki/kit"
)

// NTextViews is the number of text views to create -- to keep things simple
// and consistent (e.g., splitter settings always have the same number of
// values), we fix this degree of freedom, and have flexibility in the
// splitter settings for what to actually show.
const NTextViews = 2

// These are then the fixed indices of the different elements in the splitview
const (
	FileTreeIdx = iota
	TextView1Idx
	TextView2Idx
	MainTabsIdx
	VisTabsIdx
)

// Gide is the core editor and tab viewer framework for the Gide system.  The
// default view has a tree browser of files on the left, editor panels in the
// middle, and a tabbed viewer on the right.
type Gide struct {
	gi.Frame
	ProjRoot          gi.FileName             `desc:"root directory for the project -- all projects must be organized within a top-level root directory, with all the files therein constituting the scope of the project -- by default it is the path for ProjFilename"`
	ProjFilename      gi.FileName             `ext:".gide" desc:"current project filename for saving / loading specific Gide configuration information in a .gide file (optional)"`
	ActiveFilename    gi.FileName             `desc:"filename of the currently-active textview"`
	ActiveLang        filecat.Supported       `desc:"language for current active filename"`
	Changed           bool                    `json:"-" desc:"has the root changed?  we receive update signals from root for changes"`
	Files             giv.FileTree            `desc:"all the files in the project directory and subdirectories"`
	ActiveTextViewIdx int                     `json:"-" desc:"index of the currently-active textview -- new files will be viewed in other views if available"`
	OpenNodes         OpenNodes               `json:"-" desc:"list of open nodes, most recent first"`
	CmdBufs           map[string]*giv.TextBuf `json:"-" desc:"the command buffers for commands run in this project"`
	CmdHistory        CmdNames                `json:"-" desc:"history of commands executed in this session"`
	RunningCmds       CmdRuns                 `json:"-" xml:"-" desc:"currently running commands in this project"`
	Prefs             ProjPrefs               `desc:"preferences for this project -- this is what is saved in a .gide project file"`
	KeySeq1           key.Chord               `desc:"first key in sequence if needs2 key pressed"`
	UpdtMu            sync.Mutex              `desc:"mutex for protecting overall updates to Gide"`
}

var KiT_Gide = kit.Types.AddType(&Gide{}, nil)

func init() {
	kit.Types.SetProps(KiT_Gide, GideProps)
}

// UpdateFiles updates the list of files saved in project
func (ge *Gide) UpdateFiles() {
	ge.Files.OpenPath(string(ge.ProjRoot))
}

func (ge *Gide) IsEmpty() bool {
	return ge.ProjRoot == ""
}

// OpenRecent opens a recently-used file
func (ge *Gide) OpenRecent(filename gi.FileName) {
	ext := strings.ToLower(filepath.Ext(string(filename)))
	if ext == ".gide" {
		ge.OpenProj(filename)
	} else {
		ge.OpenPath(filename)
	}
}

// OpenPath creates a new project by opening given path, which can either be a
// specific file or a folder containing multiple files of interest -- opens in
// current Gide object if it is empty, or otherwise opens a new window.
func (ge *Gide) OpenPath(path gi.FileName) (*gi.Window, *Gide) {
	if gproj, has := CheckForProjAtPath(string(path)); has {
		return ge.OpenProj(gi.FileName(gproj))
	}
	if !ge.IsEmpty() {
		return NewGideProjPath(string(path))
	}
	ge.Defaults()
	root, pnm, fnm, ok := ProjPathParse(string(path))
	if ok {
		os.Chdir(root)
		SavedPaths.AddPath(root, gi.Prefs.SavedPathsMax)
		SavePaths()
		ge.ProjRoot = gi.FileName(root)
		ge.SetName(pnm)
		ge.Prefs.ProjFilename = gi.FileName(filepath.Join(root, pnm+".gide"))
		ge.ProjFilename = ge.Prefs.ProjFilename
		ge.Prefs.ProjRoot = ge.ProjRoot
		ge.UpdateProj()
		ge.GuessMainLang()
		ge.LangDefaults()
		ge.GuessVersCtrl()
		win := ge.ParentWindow()
		if win != nil {
			winm := "gide-" + pnm
			win.SetName(winm)
			win.SetTitle(winm)
		}
		if fnm != "" {
			ge.NextViewFile(gi.FileName(fnm))
		}
	}
	return ge.ParentWindow(), ge
}

// OpenProj opens .gide project file and its settings from given filename, in a standard
// JSON-formatted file
func (ge *Gide) OpenProj(filename gi.FileName) (*gi.Window, *Gide) {
	if !ge.IsEmpty() {
		return OpenGideProj(string(filename))
	}
	ge.Defaults()
	ge.Prefs.OpenJSON(filename)
	ge.Prefs.ProjFilename = filename // should already be set but..
	_, pnm, _, ok := ProjPathParse(string(ge.Prefs.ProjRoot))
	if ok {
		os.Chdir(string(ge.Prefs.ProjRoot))
		SavedPaths.AddPath(string(filename), gi.Prefs.SavedPathsMax)
		SavePaths()
		ge.SetName(pnm)
		ge.ApplyPrefs()
		ge.UpdateProj()
		win := ge.ParentWindow()
		if win != nil {
			winm := "gide-" + pnm
			win.SetName(winm)
			win.SetTitle(winm)
		}
	}
	return ge.ParentWindow(), ge
}

// NewProj creates a new project at given path, making a new folder in that
// path -- all Gide projects are essentially defined by a path to a folder
// containing files.  If the folder already exists, then use OpenPath.
// Can also specify main language and version control type
func (ge *Gide) NewProj(path gi.FileName, folder string, mainLang filecat.Supported, versCtrl VersCtrlName) (*gi.Window, *Gide) {
	np := filepath.Join(string(path), folder)
	err := os.MkdirAll(np, 0775)
	if err != nil {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "Couldn't Make Folder", Prompt: fmt.Sprintf("Could not make folder for project at: %v, err: %v", np, err)}, true, false, nil, nil)
		return nil, nil
	}
	win, nge := ge.OpenPath(gi.FileName(np))
	nge.Prefs.MainLang = mainLang
	if versCtrl != "" {
		nge.Prefs.VersCtrl = versCtrl
	}
	return win, nge
}

// NewFile creates a new file in the project
func (ge *Gide) NewFile(filename string) {
	np := filepath.Join(string(ge.ProjRoot), filename)
	_, err := os.Create(np)
	if err != nil {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "Couldn't Make File", Prompt: fmt.Sprintf("Could not make new file at: %v, err: %v", np, err)}, true, false, nil, nil)
		return
	}
	ge.Files.UpdateNewFile(np)
}

// SaveProj saves project file containing custom project settings, in a
// standard JSON-formatted file
func (ge *Gide) SaveProj() {
	if ge.Prefs.ProjFilename == "" {
		return
	}
	ge.SaveProjAs(ge.Prefs.ProjFilename, true) // save all files
}

// SaveProjIfExists saves project file containing custom project settings, in a
// standard JSON-formatted file, only if it already exists -- returns true if saved
// saveAllFiles indicates if user should be prompted for saving all files
func (ge *Gide) SaveProjIfExists(saveAllFiles bool) bool {
	if ge.Prefs.ProjFilename == "" {
		return false
	}
	if _, err := os.Stat(string(ge.Prefs.ProjFilename)); os.IsNotExist(err) {
		return false // does not exist
	}
	ge.SaveProjAs(ge.Prefs.ProjFilename, saveAllFiles)
	return true
}

// SaveProjAs saves project custom settings to given filename, in a standard
// JSON-formatted file
// saveAllFiles indicates if user should be prompted for saving all files
// returns true if the user was prompted, false otherwise
func (ge *Gide) SaveProjAs(filename gi.FileName, saveAllFiles bool) bool {
	SavedPaths.AddPath(string(filename), gi.Prefs.SavedPathsMax)
	SavePaths()
	ge.Files.UpdateNewFile(string(filename))
	ge.Prefs.ProjFilename = filename
	ge.ProjFilename = ge.Prefs.ProjFilename
	ge.GrabPrefs()
	ge.Prefs.SaveJSON(filename)
	gi.SaveSpellModel()
	ge.Changed = false
	if saveAllFiles {
		return ge.SaveAllCheck(false, nil) // false = no cancel option
	}
	return false
}

// SaveAllCheck -- check if any files have not been saved, and prompt to save them
// returns true if there were unsaved files, false otherwise.
// cancelOpt presents an option to cancel current command, in which case function is not called.
// if function is passed, then it is called in all cases except if the user selects cancel.
func (ge *Gide) SaveAllCheck(cancelOpt bool, fun func(ge *Gide)) bool {
	nch := ge.NChangedFiles()
	if nch == 0 {
		if fun != nil {
			fun(ge)
		}
		return false
	}
	opts := []string{"Save All", "Don't Save"}
	if cancelOpt {
		opts = []string{"Save All", "Don't Save", "Cancel Command"}
	}
	gi.ChoiceDialog(ge.Viewport, gi.DlgOpts{Title: "There are Unsaved Files",
		Prompt: fmt.Sprintf("In Project: %v There are <b>%v</b> opened files with <b>unsaved changes</b> -- do you want to save all?", ge.Nm, nch)}, opts,
		ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			if sig != 2 {
				if sig == 0 {
					ge.SaveAllOpenNodes()
				}
				if fun != nil {
					fun(ge)
				}
			}
		})
	return true
}

// UpdateProj does full update to current proj
func (ge *Gide) UpdateProj() {
	mods, updt := ge.StdConfig()
	if !mods {
		updt = ge.UpdateStart()
	}
	ge.UpdateFiles()
	ge.ConfigSplitView()
	ge.ConfigToolbar()
	ge.ConfigStatusBar()
	ge.SetStatus("just updated")
	if mods {
		ge.OpenConsoleTab()
	}
	ge.UpdateEnd(updt)
}

// ProjPathParse parses given project path into a root directory (which could
// be the path or just the directory portion of the path, depending in whether
// the path is a directory or not), and a bool if all is good (otherwise error
// message has been reported). projnm is always the last directory of the path.
func ProjPathParse(path string) (root, projnm, fnm string, ok bool) {
	if path == "" {
		return "", "blank", "", false
	}
	info, err := os.Lstat(path)
	if err != nil {
		emsg := fmt.Errorf("gide.ProjPathParse: Cannot open at given path: %q: Error: %v", path, err)
		log.Println(emsg)
		return
	}
	path, _ = filepath.Abs(path)
	dir, fn := filepath.Split(path)
	pathIsDir := info.IsDir()
	if pathIsDir {
		root = path
	} else {
		root = filepath.Clean(dir)
		fnm = fn
	}
	_, projnm = filepath.Split(root)
	ok = true
	return
}

// CheckForProjAtPath checks if there is a .gide project at the given path
// returns project path and true if found, otherwise false
func CheckForProjAtPath(path string) (string, bool) {
	root, pnm, _, ok := ProjPathParse(path)
	if !ok {
		return "", false
	}
	gproj := filepath.Join(root, pnm+".gide")
	if _, err := os.Stat(gproj); os.IsNotExist(err) {
		return "", false // does not exist
	}
	return gproj, true
}

// GuessMainLang guesses the main language in the project -- returns true if successful
func (ge *Gide) GuessMainLang() bool {
	ecs := ge.Files.FileExtCounts()
	for _, ec := range ecs {
		ls := filecat.ExtSupported(ec.Name)
		if ls != filecat.NoSupport {
			ge.Prefs.MainLang = ls
			return true
		}
	}
	return false
}

// LangDefaults applies default language settings based on MainLang
func (ge *Gide) LangDefaults() bool {
	ge.Prefs.RunCmds = CmdNames{"Run Proj"}
	ge.Prefs.BuildDir = ge.Prefs.ProjRoot
	ge.Prefs.BuildTarg = ge.Prefs.ProjRoot
	ge.Prefs.RunExec = gi.FileName(filepath.Join(string(ge.Prefs.ProjRoot), ge.Nm))
	got := false
	switch ge.Prefs.MainLang {
	case filecat.Go:
		ge.Prefs.BuildCmds = CmdNames{"Build Go Proj"}
		got = true
	case filecat.TeX:
		ge.Prefs.BuildCmds = CmdNames{"LaTeX PDF"}
		ge.Prefs.RunCmds = CmdNames{"Open Target File"}
		got = true
	default:
		ge.Prefs.BuildCmds = CmdNames{"Make"}
	}
	return got
}

// GuessVersCtrl guesses the version control system in use
func (ge *Gide) GuessVersCtrl() bool {
	got := false
	for vc, fn := range VersCtrlFiles {
		ftest := filepath.Join(string(ge.Prefs.ProjRoot), fn)
		if _, err := os.Stat(ftest); os.IsNotExist(err) {
			continue
		}
		ge.Prefs.VersCtrl = VersCtrlName(vc)
		got = true
	}
	return got
}

//////////////////////////////////////////////////////////////////////////////////////
//   TextViews

// ConfigTextBuf configures the text buf according to prefs
func (ge *Gide) ConfigTextBuf(tb *giv.TextBuf) {
	tb.SetHiStyle(Prefs.HiStyle)
	tb.Opts.TabSize = ge.Prefs.Editor.TabSize
	tb.Opts.SpaceIndent = ge.Prefs.Editor.SpaceIndent
	tb.Opts.LineNos = ge.Prefs.Editor.LineNos
	tb.Opts.AutoIndent = ge.Prefs.Editor.AutoIndent
	tb.Opts.Completion = ge.Prefs.Editor.Completion
	tb.Opts.SpellCorrect = ge.Prefs.Editor.SpellCorrect
	tb.Opts.EmacsUndo = ge.Prefs.Editor.EmacsUndo

	tb.SetSpellCorrect(tb, giv.SpellCorrectEdit) // always set -- option can override

	lang := tb.Info.Sup
	// todo: completer funcs should be stored in language struct
	switch lang {
	case filecat.Go:
		if ge.Prefs.Editor.Completion {
			tb.SetCompleter(tb, giv.CompleteGo, giv.CompleteGoEdit)
		}
	}
}

// ActiveTextView returns the currently-active TextView
func (ge *Gide) ActiveTextView() *giv.TextView {
	//	fmt.Printf("stdout: active text view idx: %v\n", ge.ActiveTextViewIdx)
	//	log.Printf("stderr: active text view idx: %v\n", ge.ActiveTextViewIdx)
	return ge.TextViewByIndex(ge.ActiveTextViewIdx)
}

// TextViewIndex finds index of given textview (0 or 1)
func (ge *Gide) TextViewIndex(av *giv.TextView) int {
	split := ge.SplitView()
	for i := 0; i < NTextViews; i++ {
		tv := split.KnownChild(TextView1Idx + i).KnownChild(0).Embed(giv.KiT_TextView).(*giv.TextView)
		if tv.This() == av.This() {
			return i
		}
	}
	return -1 // shouldn't happen
}

// TextViewForFileNode finds a TextView that is viewing given FileNode,
// and its index, or false if none is
func (ge *Gide) TextViewForFileNode(fn *giv.FileNode) (*giv.TextView, int, bool) {
	if fn.Buf == nil {
		return nil, -1, false
	}
	ge.ConfigTextBuf(fn.Buf)
	split := ge.SplitView()
	for i := 0; i < NTextViews; i++ {
		tv := split.KnownChild(TextView1Idx + i).KnownChild(0).Embed(giv.KiT_TextView).(*giv.TextView)
		if tv != nil && tv.Buf != nil && tv.Buf.This() == fn.Buf.This() && ge.PanelIsOpen(i+TextView1Idx) {
			return tv, i, true
		}
	}
	return nil, -1, false
}

// OpenNodeForTextView finds the FileNode that a given TextView is
// viewing, returning its index within OpenNodes list, or false if not found
func (ge *Gide) OpenNodeForTextView(tv *giv.TextView) (*giv.FileNode, int, bool) {
	if tv.Buf == nil {
		return nil, -1, false
	}
	for i, ond := range ge.OpenNodes {
		if ond.Buf == tv.Buf {
			return ond, i, true
		}
	}
	return nil, -1, false
}

// TextViewForFile finds FileNode for file, and returns TextView and index
// that is viewing that FileNode, or false if none is
func (ge *Gide) TextViewForFile(fnm gi.FileName) (*giv.TextView, int, bool) {
	fn, ok := ge.Files.FindFile(string(fnm))
	if !ok {
		return nil, -1, false
	}
	return ge.TextViewForFileNode(fn.This().Embed(giv.KiT_FileNode).(*giv.FileNode))
}

// SetActiveFilename sets the active file info from textbuf
func (ge *Gide) SetActiveFileInfo(buf *giv.TextBuf) {
	ge.ActiveFilename = buf.Filename
	ge.ActiveLang = buf.Info.Sup
}

// SetActiveTextView sets the given textview as the active one, and returns its index
func (ge *Gide) SetActiveTextView(av *giv.TextView) int {
	idx := ge.TextViewIndex(av)
	if idx < 0 {
		return -1
	}
	if ge.ActiveTextViewIdx == idx {
		return idx
	}
	ge.ActiveTextViewIdx = idx
	if av.Buf != nil {
		ge.SetActiveFileInfo(av.Buf)
	}
	ge.SetStatus("")
	return idx
}

// SetActiveTextViewIdx sets the given view index as the currently-active
// TextView -- returns that textview
func (ge *Gide) SetActiveTextViewIdx(idx int) *giv.TextView {
	if idx < 0 || idx >= NTextViews {
		log.Printf("Gide SetActiveTextViewIdx: text view index out of range: %v\n", idx)
		return nil
	}
	ge.ActiveTextViewIdx = idx
	av := ge.ActiveTextView()
	if av.Buf != nil {
		ge.SetActiveFileInfo(av.Buf)
	}
	ge.SetStatus("")
	av.GrabFocus()
	return av
}

// NextTextView returns the next text view available for viewing a file and
// its index -- if the active text view is empty, then it is used, otherwise
// it is the next one (if visible)
func (ge *Gide) NextTextView() (*giv.TextView, int) {
	av := ge.TextViewByIndex(ge.ActiveTextViewIdx)
	if av.Buf == nil {
		return av, ge.ActiveTextViewIdx
	}
	nxt := (ge.ActiveTextViewIdx + 1) % NTextViews
	if !ge.PanelIsOpen(nxt + TextView1Idx) {
		return av, ge.ActiveTextViewIdx
	}
	return ge.TextViewByIndex(nxt), nxt
}

// SaveActiveView saves the contents of the currently-active textview
func (ge *Gide) SaveActiveView() {
	tv := ge.ActiveTextView()
	if tv.Buf != nil {
		if tv.Buf.Filename != "" {
			tv.Buf.Save()
			ge.SetStatus("File Saved")
			fpath, _ := filepath.Split(string(tv.Buf.Filename))
			ge.Files.UpdateNewFile(fpath) // update everything in dir -- will have removed autosave
			ge.RunPostCmdsActiveView()
		} else {
			giv.CallMethod(ge, "SaveActiveViewAs", ge.Viewport) // uses fileview
		}
	}
	ge.SaveProjIfExists(false) // no saveall
}

// SaveActiveViewAs save with specified filename the contents of the
// currently-active textview
func (ge *Gide) SaveActiveViewAs(filename gi.FileName) {
	tv := ge.ActiveTextView()
	if tv.Buf != nil {
		ofn := tv.Buf.Filename
		tv.Buf.SaveAs(filename, func(canceled bool) {
			if canceled {
				ge.SetStatus(fmt.Sprintf("File %v NOT Saved As: %v", ofn, filename))
				return
			}
			ge.SetStatus(fmt.Sprintf("File %v Saved As: %v", ofn, filename))
			// ge.RunPostCmdsActiveView() // doesn't make sense..
			ge.Files.UpdateNewFile(string(filename)) // update everything in dir -- will have removed autosave
			fnk, ok := ge.Files.FindFile(string(filename))
			if ok {
				fn := fnk.This().Embed(giv.KiT_FileNode).(*giv.FileNode)
				if fn.Buf != nil {
					fn.Buf.Revert()
				}
				ge.ViewFileNode(tv, ge.ActiveTextViewIdx, fn)
			}
		})
	}
	ge.SaveProjIfExists(false) // no saveall
}

// RevertActiveView revert active view to saved version
func (ge *Gide) RevertActiveView() {
	tv := ge.ActiveTextView()
	if tv.Buf != nil {
		ge.ConfigTextBuf(tv.Buf)
		tv.Buf.Revert()
		fpath, _ := filepath.Split(string(tv.Buf.Filename))
		ge.Files.UpdateNewFile(fpath) // update everything in dir -- will have removed autosave
	}
}

// CloseActiveView closes the buffer associated with active view
func (ge *Gide) CloseActiveView() {
	tv := ge.ActiveTextView()
	ond, idx, got := ge.OpenNodeForTextView(tv)
	if got {
		ond.Buf.Close(func(canceled bool) {
			if canceled {
				ge.SetStatus(fmt.Sprintf("File %v NOT closed", ond.FPath))
				return
			}
			ge.OpenNodes.DeleteIdx(idx)
			ond.SetClosed()
			ge.SetStatus(fmt.Sprintf("File %v closed", ond.FPath))
		})
	}
}

// RunPostCmdsActiveView runs any registered post commands on the active view
// -- returns true if commands were run and file was reverted after that --
// uses MainLang to disambiguate if multiple languages associated with extension.
func (ge *Gide) RunPostCmdsActiveView() bool {
	tv := ge.ActiveTextView()
	ond, _, got := ge.OpenNodeForTextView(tv)
	if got {
		return ge.RunPostCmdsFileNode(ond)
	}
	return false
}

// RunPostCmdsFileNode runs any registered post commands on the given file node
// -- returns true if commands were run and file was reverted after that --
// uses MainLang to disambiguate if multiple languages associated with extension.
func (ge *Gide) RunPostCmdsFileNode(fn *giv.FileNode) bool {
	lang := fn.Info.Sup
	if lopt, has := AvailLangs[lang]; has {
		if len(lopt.PostSaveCmds) > 0 {
			ge.ExecCmdsFileNode(fn, lopt.PostSaveCmds, false, true) // no select, yes clear
			fn.Buf.Revert()
			return true
		}
	}
	return false
}

// AutoSaveCheck checks for an autosave file and prompts user about opening it
// -- returns true if autosave file does exist for a file that currently
// unchanged (means just opened)
func (ge *Gide) AutoSaveCheck(tv *giv.TextView, vidx int, fn *giv.FileNode) bool {
	if strings.HasPrefix(fn.Nm, "#") && strings.HasSuffix(fn.Nm, "#") {
		fn.Buf.Autosave = false
		return false // we are the autosave file
	}
	fn.Buf.Autosave = true
	if tv.IsChanged() || !fn.Buf.AutoSaveCheck() {
		return false
	}
	ge.DiffFileNode(gi.FileName(fn.Buf.AutoSaveFilename()), fn)
	gi.ChoiceDialog(ge.Viewport, gi.DlgOpts{Title: "Autosave file Exists",
		Prompt: fmt.Sprintf("An auto-save file for file: %v exists -- open it in the other text view (you can then do Save As to replace current file)?  If you don't open it, the next change made will overwrite it with a new one, erasing any changes.", fn.Nm)},
		[]string{"Open", "Ignore and Overwrite"},
		ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			switch sig {
			case 0:
				ge.NextViewFile(gi.FileName(fn.Buf.AutoSaveFilename()))
			case 1:
				fn.Buf.AutoSaveDelete()
				ge.Files.UpdateNewFile(fn.Buf.AutoSaveFilename()) // will update dir
			}
		})
	return true
}

// OpenFileNode opens file for file node -- returns new bool and error
func (ge *Gide) OpenFileNode(fn *giv.FileNode) (bool, error) {
	if fn.IsDir() {
		return false, fmt.Errorf("cannot open directory: %v", fn.FPath)
	}
	giv.FileNodeHiStyle = Prefs.HiStyle // must be set prior to OpenBuf
	nw, err := fn.OpenBuf()
	if err == nil {
		ge.ConfigTextBuf(fn.Buf)
		ge.OpenNodes.Add(fn)
		fn.SetOpen()
	}
	return nw, err
}

// ViewFileNode sets the given text view to view file in given node (opens
// buffer if not already opened)
func (ge *Gide) ViewFileNode(tv *giv.TextView, vidx int, fn *giv.FileNode) {
	if fn.IsDir() {
		return
	}
	if tv.IsChanged() {
		ge.SetStatus(fmt.Sprintf("Note: Changes not yet saved in file: %v", tv.Buf.Filename))
	}
	nw, err := ge.OpenFileNode(fn)
	if err == nil {
		tv.SetBuf(fn.Buf)
		if nw {
			ge.AutoSaveCheck(tv, vidx, fn)
		}
		ge.SetActiveTextViewIdx(vidx)
	}
}

// NextViewFileNode sets the next text view to view file in given node (opens
// buffer if not already opened) -- if already being viewed, that is
// activated, returns text view and index
func (ge *Gide) NextViewFileNode(fn *giv.FileNode) (*giv.TextView, int) {
	tv, idx, ok := ge.TextViewForFileNode(fn)
	if ok {
		ge.SetActiveTextViewIdx(idx)
		return tv, idx
	}
	nv, nidx := ge.NextTextView()
	ge.ViewFileNode(nv, nidx, fn)
	return nv, nidx
}

// NextViewFile sets the next text view to view given file name -- include as
// much of name as possible to disambiguate -- will use the first matching --
// if already being viewed, that is activated -- returns textview and its
// index, false if not found
func (ge *Gide) NextViewFile(fnm gi.FileName) (*giv.TextView, int, bool) {
	fnk, ok := ge.Files.FindFile(string(fnm))
	if !ok {
		return nil, -1, false
	}
	fn := fnk.This().Embed(giv.KiT_FileNode).(*giv.FileNode)
	if fn.IsDir() {
		return nil, -1, false
	}
	nv, nidx := ge.NextViewFileNode(fn)
	return nv, nidx, true
}

// ViewFile views file in an existing TextView if it is already viewing that
// file, otherwise opens ViewFileNode in active buffer
func (ge *Gide) ViewFile(fnm gi.FileName) (*giv.TextView, int, bool) {
	fnk, ok := ge.Files.FindFile(string(fnm))
	if !ok {
		return nil, -1, false
	}
	fn := fnk.This().Embed(giv.KiT_FileNode).(*giv.FileNode)
	if fn.IsDir() {
		return nil, -1, false
	}
	tv, idx, ok := ge.TextViewForFileNode(fn)
	if ok {
		ge.SetActiveTextViewIdx(idx)
		return tv, idx, ok
	}
	tv = ge.ActiveTextView()
	idx = ge.ActiveTextViewIdx
	ge.ViewFileNode(tv, idx, fn)
	return tv, idx, true
}

// LinkViewFileNode opens the file node in the 2nd textview, which is next to
// the tabs where links are clicked, if it is not collapsed -- else 1st
func (ge *Gide) LinkViewFileNode(fn *giv.FileNode) (*giv.TextView, int) {
	if ge.PanelIsOpen(TextView2Idx) {
		ge.SetActiveTextViewIdx(1)
	} else {
		ge.SetActiveTextViewIdx(0)
	}
	tv := ge.ActiveTextView()
	idx := ge.ActiveTextViewIdx
	ge.ViewFileNode(tv, idx, fn)
	return tv, idx
}

// LinkViewFile opens the file in the 2nd textview, which is next to
// the tabs where links are clicked, if it is not collapsed -- else 1st
func (ge *Gide) LinkViewFile(fnm gi.FileName) (*giv.TextView, int, bool) {
	fnk, ok := ge.Files.FindFile(string(fnm))
	if !ok {
		return nil, -1, false
	}
	fn := fnk.This().Embed(giv.KiT_FileNode).(*giv.FileNode)
	if fn.IsDir() {
		return nil, -1, false
	}
	nv, nidx := ge.LinkViewFileNode(fn)
	return nv, nidx, true
}

// GideOpenNodes gets list of open nodes for submenu-func
func GideOpenNodes(it interface{}, vp *gi.Viewport2D) []string {
	ge, ok := it.(ki.Ki).Embed(KiT_Gide).(*Gide)
	if !ok {
		return nil
	}
	return ge.OpenNodes.Strings()
}

// ViewOpenNodeName views given open node (by name) in active view
func (ge *Gide) ViewOpenNodeName(name string) {
	nb := ge.OpenNodes.ByStringName(name)
	if nb == nil {
		return
	}
	tv := ge.ActiveTextView()
	ge.ViewFileNode(tv, ge.ActiveTextViewIdx, nb)
}

// SelectOpenNode pops up a menu to select an open node (aka buffer) to view
// in current active textview
func (ge *Gide) SelectOpenNode() {
	if len(ge.OpenNodes) == 0 {
		ge.SetStatus("No open nodes to choose from")
		return
	}
	nl := ge.OpenNodes.Strings()
	tv := ge.ActiveTextView() // nl[0] is always currently viewed
	def := nl[0]
	if len(nl) > 1 {
		def = nl[1]
	}
	gi.StringsChooserPopup(nl, def, tv, func(recv, send ki.Ki, sig int64, data interface{}) {
		ac := send.(*gi.Action)
		idx := ac.Data.(int)
		nb := ge.OpenNodes[idx]
		ge.ViewFileNode(tv, ge.ActiveTextViewIdx, nb)
	})
}

// CloneActiveView sets the next text view to view the same file currently being vieweds
// in the active view. returns text view and index
func (ge *Gide) CloneActiveView() (*giv.TextView, int) {
	tv := ge.ActiveTextView()
	if tv == nil {
		return nil, -1
	}
	ond, _, got := ge.OpenNodeForTextView(tv)
	if got {
		nv, nidx := ge.NextTextView()
		ge.ViewFileNode(nv, nidx, ond)
		return nv, nidx
	}
	return nil, -1
}

// SaveAllOpenNodes saves all of the open filenodes to their current file names
func (ge *Gide) SaveAllOpenNodes() {
	for _, ond := range ge.OpenNodes {
		if ond.Buf.IsChanged() {
			ond.Buf.Save()
			ge.RunPostCmdsFileNode(ond)
		}
	}
}

// TextViewSig handles all signals from the textviews
func (ge *Gide) TextViewSig(tv *giv.TextView, sig giv.TextViewSignals) {
	ge.SetActiveTextView(tv) // if we're sending signals, we're the active one!
	switch sig {
	case giv.TextViewISearch:
		fallthrough
	case giv.TextViewQReplace:
		fallthrough
	case giv.TextViewCursorMoved:
		ge.SetStatus("")
	}
}

// DiffFiles shows the differences between two given files (currently outputs a context diff
// but will show a side-by-side view soon..
func (ge *Gide) DiffFiles(fnm1, fnm2 gi.FileName) {
	fnk2, ok := ge.Files.FindFile(string(fnm2))
	if !ok {
		return
	}
	fn2 := fnk2.This().Embed(giv.KiT_FileNode).(*giv.FileNode)
	if fn2.IsDir() {
		return
	}
	ge.DiffFileNode(fnm1, fn2)
}

// DiffFileNode shows the differences between two given files (currently outputs a context diff
// but will show a side-by-side view soon..
func (ge *Gide) DiffFileNode(fnm gi.FileName, fn *giv.FileNode) {
	fnk1, ok := ge.Files.FindFile(string(fnm))
	if !ok {
		return
	}
	fn1 := fnk1.This().Embed(giv.KiT_FileNode).(*giv.FileNode)
	if fn1.IsDir() {
		return
	}
	if fn1.Buf == nil {
		ge.OpenFileNode(fn1)
	}
	if fn1.Buf == nil {
		return
	}
	if fn.Buf == nil {
		ge.OpenFileNode(fn)
	}
	if fn.Buf == nil {
		return
	}
	dif := fn1.Buf.DiffBufsUnified(fn.Buf, 3)
	cbuf, _, _, _ := ge.FindOrMakeCmdTab("Diffs", true, true)
	cbuf.SetText(dif)
	cbuf.AutoScrollViews()
}

//////////////////////////////////////////////////////////////////////////////////////
//   Links

// TextLinkHandler is the Gide handler for text links -- preferred one b/c
// directly connects to correct Gide project
func TextLinkHandler(tl gi.TextLink) bool {
	ftv, _ := tl.Widget.(*giv.TextView)
	gek, ok := tl.Widget.ParentByType(KiT_Gide, true)
	if ok {
		ge := gek.Embed(KiT_Gide).(*Gide)
		ur := tl.URL
		// todo: use net/url package for more systematic parsing
		switch {
		case strings.HasPrefix(ur, "find:///"):
			ge.OpenFindURL(ur, ftv)
		case strings.HasPrefix(ur, "spell:///"):
			ge.OpenSpellURL(ur, ftv)
		case strings.HasPrefix(ur, "file:///"):
			ge.OpenFileURL(ur)
		default:
			oswin.TheApp.OpenURL(ur)
		}
	} else {
		oswin.TheApp.OpenURL(tl.URL)
	}
	return true
}

// // URLHandler is the Gide handler for urls --
// func URLHandler(url string) bool {
// 	return true
// }

// OpenFileURL opens given file:/// url
func (ge *Gide) OpenFileURL(ur string) bool {
	up, err := url.Parse(ur)
	if err != nil {
		log.Printf("Gide OpenFileURL parse err: %v\n", err)
		return false
	}
	fpath := up.Path[1:] // has double //
	pos := up.Fragment
	tv, _, ok := ge.LinkViewFile(gi.FileName(fpath))
	if !ok {
		_, fnm := filepath.Split(fpath)
		tv, _, ok = ge.LinkViewFile(gi.FileName(fnm))
		if !ok {
			gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "Couldn't Open File at Link", Prompt: fmt.Sprintf("Could not find or open file path in project: %v", fpath)}, true, false, nil, nil)
			return false
		}
	}
	if pos == "" {
		return true
	}
	// fmt.Printf("pos: %v\n", pos)
	txpos := giv.TextPos{}
	if txpos.FromString(pos) {
		reg := giv.TextRegion{Start: txpos, End: giv.TextPos{Ln: txpos.Ln, Ch: txpos.Ch + 4}}
		tv.HighlightRegion(reg)
		tv.SetCursorShow(txpos)
	}
	return true
}

func init() {
	// gi.URLHandler = URLHandler
	gi.TextLinkHandler = TextLinkHandler
}

//////////////////////////////////////////////////////////////////////////////////////
//   Close / Quit Req

// NChangedFiles returns number of opened files with unsaved changes
func (ge *Gide) NChangedFiles() int {
	return ge.OpenNodes.NChanged()
}

// CurPanel returns the splitter panel that currently has keyboard focus
// CloseWindowReq is called when user tries to close window -- we
// automatically save the project if it already exists (no harm), and prompt
// to save open files -- if this returns true, then it is OK to close --
// otherwise not
func (ge *Gide) CloseWindowReq() bool {
	ge.SaveProjIfExists(false) // don't prompt here, as we will do it now..
	nch := ge.NChangedFiles()
	if nch == 0 {
		return true
	}
	gi.ChoiceDialog(ge.Viewport, gi.DlgOpts{Title: "Close Project: There are Unsaved Files",
		Prompt: fmt.Sprintf("In Project: %v There are <b>%v</b> opened files with <b>unsaved changes</b> -- do you want to save all or cancel closing this project and review  / save those files first?", ge.Nm, nch)},
		[]string{"Cancel", "Save All", "Close Without Saving"},
		ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			switch sig {
			case 0:
				// do nothing, will have returned false already
			case 1:
				ge.SaveAllOpenNodes()
			case 2:
				ge.ParentWindow().OSWin.Close() // will not be prompted again!
			}
		})
	return false // not yet
}

// QuitReq is called when user tries to quit the app -- we go through all open
// main windows and look for gide windows and call their CloseWindowReq
// functions!
func QuitReq() bool {
	for _, win := range gi.MainWindows {
		if !strings.HasPrefix(win.Nm, "gide-") {
			continue
		}
		mfr, ok := win.MainWidget()
		if !ok {
			continue
		}
		gek, ok := mfr.ChildByName("gide", 0)
		if !ok {
			continue
		}
		ge := gek.Embed(KiT_Gide).(*Gide)
		if !ge.CloseWindowReq() {
			return false
		}
	}
	return true
}

//////////////////////////////////////////////////////////////////////////////////////
//   Panels

// PanelIsOpen returns true if the given panel has not been collapsed and is avail
// and visible for displaying something
func (ge *Gide) PanelIsOpen(panel int) bool {
	sv := ge.SplitView()
	if sv == nil {
		return false
	}
	if panel < 0 || panel >= len(sv.Kids) {
		return false
	}
	if sv.Splits[panel] <= 0.01 {
		return false
	}
	return true
}

// CurPanel returns the splitter panel that currently has keyboard focus
func (ge *Gide) CurPanel() int {
	sv := ge.SplitView()
	if sv == nil {
		return -1
	}
	for i, ski := range sv.Kids {
		_, sk := gi.KiToNode2D(ski)
		if sk.ContainsFocus() {
			return i
		}
	}
	return -1 // nobody
}

// FocusOnPanel moves keyboard focus to given panel -- returns false if nothing at that tab
func (ge *Gide) FocusOnPanel(panel int) bool {
	sv := ge.SplitView()
	if sv == nil {
		return false
	}
	win := ge.ParentWindow()
	switch panel {
	case TextView1Idx:
		ge.SetActiveTextViewIdx(0)
	case TextView2Idx:
		ge.SetActiveTextViewIdx(1)
	case MainTabsIdx:
		tv := ge.MainTabs()
		ct, _, has := tv.CurTab()
		if has {
			win.FocusNext(ct)
		} else {
			return false
		}
	case VisTabsIdx:
		tv := ge.VisTabs()
		ct, _, has := tv.CurTab()
		if has {
			win.FocusNext(ct)
		} else {
			return false
		}
	default:
		ski := sv.Kids[panel]
		win.FocusNext(ski)
	}
	return true
}

// FocusNextPanel moves the keyboard focus to the next panel to the right
func (ge *Gide) FocusNextPanel() {
	sv := ge.SplitView()
	if sv == nil {
		return
	}
	cp := ge.CurPanel()
	cp++
	np := len(sv.Kids)
	if cp >= np {
		cp = 0
	}
	for sv.Splits[cp] <= 0.01 {
		cp++
		if cp >= np {
			cp = 0
		}
	}
	ge.FocusOnPanel(cp)
}

// FocusPrevPanel moves the keyboard focus to the previous panel to the left
func (ge *Gide) FocusPrevPanel() {
	sv := ge.SplitView()
	if sv == nil {
		return
	}
	cp := ge.CurPanel()
	cp--
	np := len(sv.Kids)
	if cp < 0 {
		cp = np - 1
	}
	for sv.Splits[cp] <= 0.01 {
		cp--
		if cp < 0 {
			cp = np - 1
		}
	}
	ge.FocusOnPanel(cp)
}

//////////////////////////////////////////////////////////////////////////////////////
//    Tabs

// MainTabByName returns a MainTabs (first set of tabs) tab with given name,
// and its index -- returns false if not found
func (ge *Gide) MainTabByName(label string) (gi.Node2D, int, bool) {
	tv := ge.MainTabs()
	return tv.TabByName(label)
}

// SelectMainTabByName Selects given main tab, and returns all of its contents as well.
func (ge *Gide) SelectMainTabByName(label string) (gi.Node2D, int, bool) {
	tv := ge.MainTabs()
	widg, idx, ok := ge.MainTabByName(label)
	if ok {
		tv.SelectTabIndex(idx)
	}
	return widg, idx, ok
}

// FindOrMakeMainTab returns a MainTabs (first set of tabs) tab with given
// name, first by looking for an existing one, and if not found, making a new
// one with widget of given type.  if sel, then select it.  returns widget and tab index.
func (ge *Gide) FindOrMakeMainTab(label string, typ reflect.Type, sel bool) (gi.Node2D, int) {
	tv := ge.MainTabs()
	widg, idx, ok := ge.MainTabByName(label)
	if ok {
		if sel {
			tv.SelectTabIndex(idx)
		}
		return widg, idx
	}
	widg, idx = tv.AddNewTab(typ, label)
	if sel {
		tv.SelectTabIndex(idx)
	}
	return widg, idx
}

// ConfigOutputTextView configures a command-output textview within given parent layout
func (ge *Gide) ConfigOutputTextView(ly *gi.Layout) *giv.TextView {
	ly.Lay = gi.LayoutVert
	ly.SetStretchMaxWidth()
	ly.SetStretchMaxHeight()
	ly.SetMinPrefWidth(units.NewValue(20, units.Ch))
	ly.SetMinPrefHeight(units.NewValue(10, units.Ch))
	var tv *giv.TextView
	if ly.HasChildren() {
		tv = ly.KnownChild(0).Embed(giv.KiT_TextView).(*giv.TextView)
	} else {
		tv = ly.AddNewChild(giv.KiT_TextView, ly.Nm).(*giv.TextView)
	}

	if ge.Prefs.Editor.WordWrap {
		tv.SetProp("white-space", gi.WhiteSpacePreWrap)
	} else {
		tv.SetProp("white-space", gi.WhiteSpacePre)
	}
	tv.SetProp("tab-size", 8) // std for output
	tv.SetProp("font-family", Prefs.FontFamily)
	tv.SetInactive()
	return tv
}

// FindOrMakeMainTabTextView returns a MainTabs (first set of tabs) tab with given
// name, first by looking for an existing one, and if not found, making a new
// one with a Layout and then a TextView in it.  if sel, then select it.
// returns widget and tab index.
func (ge *Gide) FindOrMakeMainTabTextView(label string, sel bool) (*giv.TextView, int) {
	lyk, idx := ge.FindOrMakeMainTab(label, gi.KiT_Layout, sel)
	ly := lyk.Embed(gi.KiT_Layout).(*gi.Layout)
	tv := ge.ConfigOutputTextView(ly)
	return tv, idx
}

// FindOrMakeCmdBuf creates the buffer for command output, or returns
// existing. If clear is true, then any existing buffer is cleared.
// Returns true if new buffer created.
func (ge *Gide) FindOrMakeCmdBuf(cmdNm string, clear bool) (*giv.TextBuf, bool) {
	if ge.CmdBufs == nil {
		ge.CmdBufs = make(map[string]*giv.TextBuf, 20)
	}
	if buf, has := ge.CmdBufs[cmdNm]; has {
		if clear {
			buf.New(0)
		}
		return buf, false
	}
	buf := &giv.TextBuf{}
	buf.InitName(buf, cmdNm+"-buf")
	ge.CmdBufs[cmdNm] = buf
	buf.Autosave = false
	return buf, true
}

// FindOrMakeCmdTab creates the tab to show command output, including making a
// buffer object to save output from the command. returns true if a new buffer
// was created, false if one already existed. if sel, select tab.  if clearBuf, then any
// existing buffer is cleared.  Also returns index of tab.
func (ge *Gide) FindOrMakeCmdTab(cmdNm string, sel bool, clearBuf bool) (*giv.TextBuf, *giv.TextView, int, bool) {
	buf, nw := ge.FindOrMakeCmdBuf(cmdNm, clearBuf)
	ctv, idx := ge.FindOrMakeMainTabTextView(cmdNm, sel)
	ctv.SetInactive()
	ctv.SetBuf(buf)
	return buf, ctv, idx, nw
}

// VisTabByName returns a VisTabs (second set of tabs for visualizations) tab
// with given name, and its index -- returns false if not found
func (ge *Gide) VisTabByName(label string) (gi.Node2D, int, bool) {
	tv := ge.VisTabs()
	if tv == nil {
		return nil, -1, false
	}
	return tv.TabByName(label)
}

// MainTabDeleted is called when a main tab is deleted -- we cancel any running commmands
func (ge *Gide) MainTabDeleted(tabnm string) {
	ge.RunningCmds.KillByName(tabnm)
}

//////////////////////////////////////////////////////////////////////////////////////
//    Commands / Tabs

// ExecCmdName executes command of given name -- this is the final common
// pathway for all command invokation except on a node.  if sel, select tab.
// if clearBuf, clear the buffer prior to command
func (ge *Gide) ExecCmdName(cmdNm CmdName, sel bool, clearBuf bool) {
	cmd, _, ok := AvailCmds.CmdByName(cmdNm, true)
	if !ok {
		return
	}
	ge.SetArgVarVals()
	cbuf, _, _, _ := ge.FindOrMakeCmdTab(cmd.Name, sel, clearBuf)
	cmd.Run(ge, cbuf)
}

// ExecCmdNameFileNode executes command of given name on given node
func (ge *Gide) ExecCmdNameFileNode(fn *giv.FileNode, cmdNm CmdName, sel bool, clearBuf bool) {
	cmd, _, ok := AvailCmds.CmdByName(cmdNm, true)
	if !ok {
		return
	}
	SetArgVarVals(&ArgVarVals, string(fn.FPath), &ge.Prefs, nil)
	cbuf, _, _, _ := ge.FindOrMakeCmdTab(cmd.Name, sel, clearBuf)
	cmd.Run(ge, cbuf)
}

// GideExecCmds gets list of available commands for current active file, as a submenu-func
func GideExecCmds(it interface{}, vp *gi.Viewport2D) []string {
	ge, ok := it.(ki.Ki).Embed(KiT_Gide).(*Gide)
	if !ok {
		return nil
	}
	tv := ge.ActiveTextView()
	if tv == nil {
		return nil
	}
	var cmds []string
	if ge.ActiveLang == filecat.NoSupport {
		cmds = AvailCmds.FilterCmdNames(ge.Prefs.MainLang, ge.Prefs.VersCtrl)
	} else {
		cmds = AvailCmds.FilterCmdNames(ge.ActiveLang, ge.Prefs.VersCtrl)
	}
	return cmds
}

// ExecCmdNameActive calls given command on current active textview
func (ge *Gide) ExecCmdNameActive(cmdNm string) {
	tv := ge.ActiveTextView()
	if tv == nil {
		return
	}
	ge.SaveAllCheck(true, func(gee *Gide) { // true = cancel option
		gee.ExecCmdName(CmdName(cmdNm), true, true)
	})
}

// ExecCmd pops up a menu to select a command appropriate for the current
// active text view, and shows output in MainTab with name of command
func (ge *Gide) ExecCmd() {
	tv := ge.ActiveTextView()
	if tv == nil {
		fmt.Printf("no Active view for ExecCmd\n")
		return
	}
	var cmds []string
	if ge.ActiveLang == filecat.NoSupport {
		cmds = AvailCmds.FilterCmdNames(ge.Prefs.MainLang, ge.Prefs.VersCtrl)
	} else {
		cmds = AvailCmds.FilterCmdNames(ge.ActiveLang, ge.Prefs.VersCtrl)
	}
	hsz := len(ge.CmdHistory)
	lastCmd := ""
	if hsz > 0 {
		lastCmd = string(ge.CmdHistory[hsz-1])
	}
	gi.StringsChooserPopup(cmds, lastCmd, tv, func(recv, send ki.Ki, sig int64, data interface{}) {
		ac := send.(*gi.Action)
		cmdNm := CmdName(ac.Text)
		ge.CmdHistory.Add(cmdNm)                // only save commands executed via chooser
		ge.SaveAllCheck(true, func(gee *Gide) { // true = cancel option
			gee.ExecCmdName(cmdNm, true, true) // sel, clear
		})
	})
}

// ExecCmdFileNode pops up a menu to select a command appropriate for the given node,
// and shows output in MainTab with name of command
func (ge *Gide) ExecCmdFileNode(fn *giv.FileNode) {
	lang := fn.Info.Sup
	cmds := AvailCmds.FilterCmdNames(lang, ge.Prefs.VersCtrl)
	gi.StringsChooserPopup(cmds, "", ge, func(recv, send ki.Ki, sig int64, data interface{}) {
		ac := send.(*gi.Action)
		ge.ExecCmdNameFileNode(fn, CmdName(ac.Text), true, true) // sel, clearbuf
	})
}

// SetArgVarVals sets the ArgVar values for commands, from Gide values
func (ge *Gide) SetArgVarVals() {
	tv := ge.ActiveTextView()
	if tv == nil || tv.Buf == nil {
		SetArgVarVals(&ArgVarVals, "", &ge.Prefs, tv)
	} else {
		SetArgVarVals(&ArgVarVals, string(tv.Buf.Filename), &ge.Prefs, tv)
	}
}

// ExecCmds executes a sequence of commands, sel = select tab, clearBuf = clear buffer
func (ge *Gide) ExecCmds(cmdNms CmdNames, sel bool, clearBuf bool) {
	for _, cmdNm := range cmdNms {
		ge.ExecCmdName(cmdNm, sel, clearBuf)
	}
}

// ExecCmdsFileNode executes a sequence of commands on file node, sel = select tab, clearBuf = clear buffer
func (ge *Gide) ExecCmdsFileNode(fn *giv.FileNode, cmdNms CmdNames, sel bool, clearBuf bool) {
	for _, cmdNm := range cmdNms {
		ge.ExecCmdNameFileNode(fn, cmdNm, sel, clearBuf)
	}
}

// Build runs the BuildCmds set for this project
func (ge *Gide) Build() {
	if len(ge.Prefs.BuildCmds) == 0 {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "No BuildCmds Set", Prompt: fmt.Sprintf("You need to set the BuildCmds in the Project Preferences")}, true, false, nil, nil)
		return
	}
	ge.SaveAllCheck(true, func(gee *Gide) { // true = cancel option
		gee.ExecCmds(ge.Prefs.BuildCmds, true, true)
	})
}

// Run runs the RunCmds set for this project
func (ge *Gide) Run() {
	if len(ge.Prefs.RunCmds) == 0 {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "No RunCmds Set", Prompt: fmt.Sprintf("You need to set the RunCmds in the Project Preferences")}, true, false, nil, nil)
		return
	}
	ge.ExecCmds(ge.Prefs.RunCmds, true, true)
}

// Commit commits the current changes using relevant VCS tool, and updates the changelog.
// Checks for VCS setting and
func (ge *Gide) Commit() {
	if ge.Prefs.VersCtrl == "" {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "No VersCtrl Set", Prompt: fmt.Sprintf("You need to set the VersCtrl in the Project Preferences")}, true, false, nil, nil)
		return
	}

	ge.SaveAllCheck(true, func(gee *Gide) { // true = cancel option
		ge.CommitNoChecks()
	})
}

// CommitNoChecks does the commit without any further checks for VCS, and unsaved files
func (ge *Gide) CommitNoChecks() {
	cmds := AvailCmds.FilterCmdNames(ge.ActiveLang, ge.Prefs.VersCtrl)
	cmdnm := ""
	for _, cm := range cmds {
		if strings.Contains(cm, "Commit") {
			cmdnm = cm
			break
		}
	}
	if cmdnm == "" {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "No Commit command found", Prompt: fmt.Sprintf("Could not find Commit command in list of avail commands -- this is usually a programmer error -- check preferences settings etc")}, true, false, nil, nil)
		return
	}
	ge.SetArgVarVals() // need to set before setting prompt string below..

	gi.StringPromptDialog(ge.Viewport, "", "Enter commit message here..",
		gi.DlgOpts{Title: "Commit Message", Prompt: "Please enter your commit message here -- this will be recorded along with other information from the commit in the project's ChangeLog, which can be viewed under Proj Prefs menu item -- author information comes from User settings in GoGi Preferences."},
		ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			dlg := send.(*gi.Dialog)
			if sig == int64(gi.DialogAccepted) {
				msg := gi.StringPromptDialogValue(dlg)
				ArgVarVals["{PromptString1}"] = msg
				CmdNoUserPrompt = true // don't re-prompt!
				ge.Prefs.ChangeLog.Add(ChangeRec{Date: giv.FileTime(time.Now()), Author: gi.Prefs.User.Name, Email: gi.Prefs.User.Email, Message: msg})
				ge.ExecCmdName(CmdName(cmdnm), true, true) // must be wait
				ge.CommitUpdtLog(cmdnm)
			}
		})
}

// CommitUpdtLog grabs info from buffer in main tabs about the commit, and
// updates the changelog record
func (ge *Gide) CommitUpdtLog(cmdnm string) {
	ctv, _ := ge.FindOrMakeMainTabTextView(cmdnm, false) // don't sel
	if ctv == nil {
		return
	}
	if ctv.Buf == nil {
		return
	}
	// todo: process text!
	ge.SaveProjIfExists(true) // saveall
}

// OpenConsoleTab opens a main tab displaying console output (stdout, stderr)
func (ge *Gide) OpenConsoleTab() {
	ctv, _ := ge.FindOrMakeMainTabTextView("Console", true)
	ctv.SetInactive()
	if ctv.Buf == nil || ctv.Buf != TheConsole.Buf {
		ctv.SetBuf(TheConsole.Buf)
		TheConsole.Buf.TextBufSig.Connect(ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			gee, _ := recv.Embed(KiT_Gide).(*Gide)
			gee.SelectMainTabByName("Console")
		})
	}
}

//////////////////////////////////////////////////////////////////////////////////////
//    TextView functions

// CursorToHistPrev moves cursor to previous position on history list --
// returns true if moved
func (ge *Gide) CursorToHistPrev() bool {
	tv := ge.ActiveTextView()
	return tv.CursorToHistPrev()
}

// CursorToHistNext moves cursor to next position on history list --
// returns true if moved
func (ge *Gide) CursorToHistNext() bool {
	tv := ge.ActiveTextView()
	return tv.CursorToHistNext()
}

//////////////////////////////////////////////////////////////////////////////////////
//    Find / Replace

// Find does Find / Replace in files, using given options and filters -- opens up a
// main tab with the results and further controls.
func (ge *Gide) Find(find, repl string, ignoreCase bool, loc FindLoc, langs []filecat.Supported) {
	if find == "" {
		return
	}
	ge.Prefs.Find.IgnoreCase = ignoreCase
	ge.Prefs.Find.Langs = langs
	ge.Prefs.Find.Loc = loc

	fbuf, _ := ge.FindOrMakeCmdBuf("Find", true)
	fvi, _ := ge.FindOrMakeMainTab("Find", KiT_FindView, true) // sel
	fv := fvi.Embed(KiT_FindView).(*FindView)
	fv.UpdateView(ge)
	fv.Time = time.Now()
	ftv := fv.TextView()
	ftv.SetInactive()
	ftv.SetBuf(fbuf)

	fv.SaveFindString(find)
	fv.SaveReplString(repl)

	root := ge.Files.Embed(giv.KiT_FileNode).(*giv.FileNode)

	atv := ge.ActiveTextView()
	ond, _, got := ge.OpenNodeForTextView(atv)
	adir := ""
	if got {
		adir, _ = filepath.Split(string(ond.FPath))
	}

	var res []FileSearchResults
	if loc == FindLocFile {
		if got {
			cnt, matches := atv.Buf.Search([]byte(find), ignoreCase)
			res = append(res, FileSearchResults{ond, cnt, matches})
		}
	} else {
		res = FileTreeSearch(root, find, ignoreCase, loc, adir, langs)
	}

	outlns := make([][]byte, 0, 100)
	outmus := make([][]byte, 0, 100) // markups
	for _, fs := range res {
		fp := fs.Node.Info.Path
		fn := fs.Node.MyRelPath()
		fbStLn := len(outlns) // find buf start ln
		lstr := fmt.Sprintf(`%v: %v`, fn, fs.Count)
		outlns = append(outlns, []byte(lstr))
		mstr := fmt.Sprintf(`<b>%v</b>`, lstr)
		outmus = append(outmus, []byte(mstr))
		for _, mt := range fs.Matches {
			ln := mt.Reg.Start.Ln + 1
			ch := mt.Reg.Start.Ch + 1
			ech := mt.Reg.End.Ch + 1
			fnstr := fmt.Sprintf("%v:%d:%d", fn, ln, ch)
			nomu := bytes.Replace(mt.Text, []byte("<mark>"), nil, -1)
			nomu = bytes.Replace(nomu, []byte("</mark>"), nil, -1)
			nomus := html.EscapeString(string(nomu))
			lstr = fmt.Sprintf(`%v: %s`, fnstr, nomus) // note: has tab embedded at start of lstr

			outlns = append(outlns, []byte(lstr))
			mstr = fmt.Sprintf(`	<a href="find:///%v#R%vN%vL%vC%v-L%vC%v">%v</a>: %s`, fp, fbStLn, fs.Count, ln, ch, ln, ech, fnstr, mt.Text)
			outmus = append(outmus, []byte(mstr))
		}
		outlns = append(outlns, []byte(""))
		outmus = append(outmus, []byte(""))
	}
	ltxt := bytes.Join(outlns, []byte("\n"))
	mtxt := bytes.Join(outmus, []byte("\n"))
	fbuf.AppendTextMarkup(ltxt, mtxt, false, true) // no save undo, yes signal
	ftv.CursorStartDoc()
	ok := ftv.CursorNextLink(false) // no wrap
	if ok {
		ftv.OpenLinkAt(ftv.CursorPos)
	}
	ge.FocusOnPanel(MainTabsIdx)
}

// Spell checks spelling in files
func (ge *Gide) Spell() {
	fbuf, _ := ge.FindOrMakeCmdBuf("Spell", true)
	svi, _ := ge.FindOrMakeMainTab("Spell", KiT_SpellView, true) // sel
	sv := svi.Embed(KiT_SpellView).(*SpellView)
	sv.UpdateView(ge, ge.Prefs.Spell)
	stv := sv.TextView()
	stv.SetInactive()
	stv.SetBuf(fbuf)

	fp := string(ge.ActiveFilename)
	if fp == "" {
		return
	}

	tv := ge.ActiveTextView()
	gi.InitSpell()
	text := tv.Buf.LinesToBytesCopy()
	gi.InitNewSpellCheck(text)
	tw, suggests, err := gi.NextUnknownWord()
	if err != nil {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "Error Running Spell Check", Prompt: fmt.Sprintf("%v", err)}, true, false, nil, nil)
	}
	sv.SetUnknownAndSuggest(tw, suggests)
	ge.FocusOnPanel(MainTabsIdx)
}

// ParseOpenFindURL parses and opens given find:/// url from Find, return text
// region encoded in url, and starting line of results in find buffer, and
// number of results returned -- for parsing all the find results
func (ge *Gide) ParseOpenFindURL(ur string, ftv *giv.TextView) (tv *giv.TextView, reg giv.TextRegion, findBufStLn, findCount int, ok bool) {
	up, err := url.Parse(ur)
	if err != nil {
		log.Printf("FindView OpenFindURL parse err: %v\n", err)
		return
	}
	fpath := up.Path[1:] // has double //
	pos := up.Fragment
	tv, _, ok = ge.LinkViewFile(gi.FileName(fpath))
	if !ok {
		gi.PromptDialog(ge.Viewport, gi.DlgOpts{Title: "Couldn't Open File at Link", Prompt: fmt.Sprintf("Could not find or open file path in project: %v", fpath)}, true, false, nil, nil)
		return
	}
	if pos == "" {
		return
	}

	lidx := strings.Index(pos, "L")
	if lidx > 0 {
		reg.FromString(pos[lidx:])
		pos = pos[:lidx]
	}
	fmt.Sscanf(pos, "R%dN%d", &findBufStLn, &findCount)
	return
}

// OpenFindURL opens given find:/// url from Find -- delegates to FindView
func (ge *Gide) OpenFindURL(ur string, ftv *giv.TextView) bool {
	fvk, ok := ftv.ParentByType(KiT_FindView, true)
	if !ok {
		return false
	}
	fv := fvk.(*FindView)
	return fv.OpenFindURL(ur, ftv)
}

// OpenSpellURL opens given spell:/// url from Spell -- delegates to SpellView
func (ge *Gide) OpenSpellURL(ur string, stv *giv.TextView) bool {
	svk, ok := stv.ParentByType(KiT_SpellView, true)
	if !ok {
		return false
	}
	fv := svk.(*SpellView)
	return fv.OpenSpellURL(ur, stv)
}

// ReplaceInActive does query-replace in active file only
func (ge *Gide) ReplaceInActive() {
	tv := ge.ActiveTextView()
	tv.QReplacePrompt()
}

//////////////////////////////////////////////////////////////////////////////////////
//    Registers

// RegisterCopy saves current selection in active text view to register of given name
// returns true if saved
func (ge *Gide) RegisterCopy(name string) bool {
	if name == "" {
		return false
	}
	tv := ge.ActiveTextView()
	if tv.Buf == nil {
		return false
	}
	sel := tv.Selection()
	if sel == nil {
		return false
	}
	if AvailRegisters == nil {
		AvailRegisters = make(Registers, 100)
	}
	AvailRegisters[name] = string(sel.ToBytes())
	AvailRegisters.SavePrefs()
	ge.Prefs.Register = RegisterName(name)
	tv.SelectReset()
	return true
}

// RegisterPaste pastes register of given name into active text view
// returns true if pasted
func (ge *Gide) RegisterPaste(name RegisterName) bool {
	if name == "" {
		return false
	}
	str, ok := AvailRegisters[string(name)]
	if !ok {
		return false
	}
	tv := ge.ActiveTextView()
	if tv.Buf == nil {
		return false
	}
	tv.InsertAtCursor([]byte(str))
	ge.Prefs.Register = name
	return true
}

// CommentOut comments-out selected lines in active text view
// and uncomments if already commented
// If multiple lines are selected and any line is uncommented all will be commented
func (ge *Gide) CommentOut() bool {
	tv := ge.ActiveTextView()
	if tv.Buf == nil {
		return false
	}
	sel := tv.Selection()
	var stl, etl int
	if sel == nil {
		stl = tv.CursorPos.Ln
		etl = stl + 1
	} else {
		stl = sel.Reg.Start.Ln
		etl = sel.Reg.End.Ln
	}
	tv.Buf.CommentRegion(stl, etl)
	tv.SelectReset()
	return true
}

// Indent indents selected lines in active view
func (ge *Gide) Indent() bool {
	tv := ge.ActiveTextView()
	if tv.Buf == nil {
		return false
	}
	sel := tv.Selection()
	if sel == nil {
		return false
	}
	// todo: add indent chars to langs
	tv.Buf.AutoIndentRegion(sel.Reg.Start.Ln, sel.Reg.End.Ln, giv.DefaultIndentStrings, giv.DefaultUnindentStrings)
	tv.SelectReset()
	return true
}

//////////////////////////////////////////////////////////////////////////////////////
//    StatusBar

// SetStatus updates the statusbar label with given message, along with other status info
func (ge *Gide) SetStatus(msg string) {
	sb := ge.StatusBar()
	if sb == nil {
		return
	}
	// ge.UpdtMu.Lock()
	// defer ge.UpdtMu.Unlock()

	updt := sb.UpdateStart()
	lbl := ge.StatusLabel()
	fnm := ""
	ln := 0
	ch := 0
	tv := ge.ActiveTextView()
	if tv != nil {
		ln = tv.CursorPos.Ln + 1
		ch = tv.CursorPos.Ch
		if tv.Buf != nil {
			fnm = ge.Files.RelPath(tv.Buf.Filename)
			if tv.Buf.IsChanged() {
				fnm += "*"
			}
		}
		if tv.ISearch.On {
			msg = fmt.Sprintf("\tISearch: %v (n=%v)\t%v", tv.ISearch.Find, len(tv.ISearch.Matches), msg)
		}
		if tv.QReplace.On {
			msg = fmt.Sprintf("\tQReplace: %v -> %v (n=%v)\t%v", tv.QReplace.Find, tv.QReplace.Replace, len(tv.QReplace.Matches), msg)
		}
	}

	str := fmt.Sprintf("%v\t<b>%v:</b>\t(%v,%v)\t%v", ge.Nm, fnm, ln, ch, msg)
	lbl.SetText(str)
	sb.UpdateEnd(updt)
}

//////////////////////////////////////////////////////////////////////////////////////
//    Defaults, Prefs

// Defaults sets new project defaults based on overall preferences
func (ge *Gide) Defaults() {
	ge.Prefs.Files = Prefs.Files
	ge.Prefs.Editor = Prefs.Editor
	ge.Prefs.Splits = []float32{.1, .325, .325, .25, 0}
	ge.Files.DirsOnTop = ge.Prefs.Files.DirsOnTop
	ge.Files.NodeType = KiT_FileNode
}

// GrabPrefs grabs the current project preference settings from various
// places, e.g., prior to saving or editing.
func (ge *Gide) GrabPrefs() {
	sv := ge.SplitView()
	if sv != nil {
		ge.Prefs.Splits = sv.Splits
	}
	ge.Prefs.OpenDirs = ge.Files.OpenDirs
}

// ApplyPrefs applies current project preference settings into places where
// they are used -- only for those done prior to loading
func (ge *Gide) ApplyPrefs() {
	ge.ProjFilename = ge.Prefs.ProjFilename
	ge.ProjRoot = ge.Prefs.ProjRoot
	ge.Files.OpenDirs = ge.Prefs.OpenDirs
	ge.Files.DirsOnTop = ge.Prefs.Files.DirsOnTop
	histyle.StyleDefault = Prefs.HiStyle
	sv := ge.SplitView()
	if sv != nil {
		for i := 0; i < NTextViews; i++ {
			txly := sv.KnownChild(1 + i).(*gi.Layout)
			txed := txly.KnownChild(0).(*giv.TextView)
			if txed.Buf != nil {
				ge.ConfigTextBuf(txed.Buf)
			}
		}
		for _, ond := range ge.OpenNodes {
			if ond.Buf != nil {
				ge.ConfigTextBuf(ond.Buf)
			}
		}
	}
}

// ApplyPrefsAction applies current preferences to the project, and updates the project
func (ge *Gide) ApplyPrefsAction() {
	ge.ApplyPrefs()
	ge.SetFullReRender()
	ge.UpdateProj()
}

// ProjPrefs allows editing of project preferences (settings specific to this project)
func (ge *Gide) ProjPrefs() {
	sv, _ := ProjPrefsView(&ge.Prefs)
	// we connect to changes and apply them
	sv.ViewSig.Connect(ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
		gee, _ := recv.Embed(KiT_Gide).(*Gide)
		gee.ApplyPrefsAction()
	})
}

// SplitsSetView sets split view splitters to given named setting
func (ge *Gide) SplitsSetView(split SplitName) {
	sv := ge.SplitView()
	if sv != nil {
		sp, _, ok := AvailSplits.SplitByName(split)
		if ok {
			sv.SetSplitsAction(sp.Splits...)
			ge.Prefs.SplitName = split
			if !ge.PanelIsOpen(ge.ActiveTextViewIdx + TextView1Idx) {
				ge.SetActiveTextViewIdx((ge.ActiveTextViewIdx + 1) % 2)
			}
		}
	}
}

// SplitsSave saves current splitter settings to named splitter settings under
// existing name, and saves to prefs file
func (ge *Gide) SplitsSave(split SplitName) {
	sv := ge.SplitView()
	if sv != nil {
		sp, _, ok := AvailSplits.SplitByName(split)
		if ok {
			sp.SaveSplits(sv.Splits)
			AvailSplits.SavePrefs()
		}
	}
}

// SplitsSaveAs saves current splitter settings to new named splitter settings, and
// saves to prefs file
func (ge *Gide) SplitsSaveAs(name, desc string) {
	sv := ge.SplitView()
	if sv != nil {
		AvailSplits.Add(name, desc, sv.Splits)
		AvailSplits.SavePrefs()
	}
}

// SplitsEdit opens the SplitsView editor to customize saved splitter settings
func (ge *Gide) SplitsEdit() {
	SplitsView(&AvailSplits)
}

// HelpWiki opens wiki page for gide on github
func (ge *Gide) HelpWiki() {
	oswin.TheApp.OpenURL("https://github.com/goki/gide/wiki")
}

//////////////////////////////////////////////////////////////////////////////////////
//   GUI configs

// StdFrameConfig returns a TypeAndNameList for configuring a standard Frame
// -- can modify as desired before calling ConfigChildren on Frame using this
func (ge *Gide) StdFrameConfig() kit.TypeAndNameList {
	config := kit.TypeAndNameList{}
	config.Add(gi.KiT_ToolBar, "toolbar")
	config.Add(gi.KiT_SplitView, "splitview")
	config.Add(gi.KiT_Frame, "statusbar")
	return config
}

// StdConfig configures a standard setup of the overall Frame -- returns mods,
// updt from ConfigChildren and does NOT call UpdateEnd
func (ge *Gide) StdConfig() (mods, updt bool) {
	ge.Lay = gi.LayoutVert
	ge.SetProp("spacing", gi.StdDialogVSpaceUnits)
	config := ge.StdFrameConfig()
	mods, updt = ge.ConfigChildren(config, false)
	return
}

// SplitView returns the main SplitView
func (ge *Gide) SplitView() *gi.SplitView {
	svi, ok := ge.ChildByName("splitview", 2)
	if !ok {
		return nil
	}
	return svi.(*gi.SplitView)
}

// FileTree returns the main FileTree
func (ge *Gide) FileTree() *giv.TreeView {
	split := ge.SplitView()
	if split != nil {
		tv := split.KnownChild(FileTreeIdx).KnownChild(0).(*giv.TreeView)
		return tv
	}
	return nil
}

// TextViewByIndex returns the TextView by index (0 or 1), nil if not found
func (ge *Gide) TextViewByIndex(idx int) *giv.TextView {
	if idx < 0 || idx >= NTextViews {
		log.Printf("Gide: text view index out of range: %v\n", idx)
		return nil
	}
	split := ge.SplitView()
	if split != nil {
		svk := split.KnownChild(TextView1Idx + idx).KnownChild(0)
		return svk.Embed(giv.KiT_TextView).(*giv.TextView)
	}
	return nil
}

// MainTabs returns the main TabView
func (ge *Gide) MainTabs() *gi.TabView {
	split := ge.SplitView()
	if split != nil {
		tv := split.KnownChild(MainTabsIdx).Embed(gi.KiT_TabView).(*gi.TabView)
		return tv
	}
	return nil
}

// VisTabs returns the second, visualization TabView
func (ge *Gide) VisTabs() *gi.TabView {
	split := ge.SplitView()
	if split != nil {
		tv := split.KnownChild(VisTabsIdx).Embed(gi.KiT_TabView).(*gi.TabView)
		return tv
	}
	return nil
}

// ToolBar returns the main toolbar
func (ge *Gide) ToolBar() *gi.ToolBar {
	tbi, ok := ge.ChildByName("toolbar", 2)
	if !ok {
		return nil
	}
	return tbi.(*gi.ToolBar)
}

// StatusBar returns the statusbar widget
func (ge *Gide) StatusBar() *gi.Frame {
	tbi, ok := ge.ChildByName("statusbar", 2)
	if !ok {
		return nil
	}
	return tbi.(*gi.Frame)
}

// StatusLabel returns the statusbar label widget
func (ge *Gide) StatusLabel() *gi.Label {
	sb := ge.StatusBar()
	if sb != nil {
		return sb.KnownChild(0).Embed(gi.KiT_Label).(*gi.Label)
	}
	return nil
}

// ConfigStatusBar configures statusbar with label
func (ge *Gide) ConfigStatusBar() {
	sb := ge.StatusBar()
	if sb == nil || sb.HasChildren() {
		return
	}
	sb.SetStretchMaxWidth()
	sb.SetMinPrefHeight(units.NewValue(1.2, units.Em))
	sb.SetProp("overflow", "hidden") // no scrollbars!
	sb.SetProp("margin", 0)
	sb.SetProp("padding", 0)
	lbl := sb.AddNewChild(gi.KiT_Label, "sb-lbl").(*gi.Label)
	lbl.SetStretchMaxWidth()
	lbl.SetMinPrefHeight(units.NewValue(1, units.Em))
	lbl.SetProp("vertical-align", gi.AlignTop)
	lbl.SetProp("margin", 0)
	lbl.SetProp("padding", 0)
	lbl.SetProp("tab-size", 4)
}

// ConfigToolbar adds a Gide toolbar.
func (ge *Gide) ConfigToolbar() {
	tb := ge.ToolBar()
	if tb.HasChildren() {
		return
	}
	tb.SetStretchMaxWidth()
	giv.ToolBarView(ge, ge.Viewport, tb)
}

// SplitViewConfig returns a TypeAndNameList for configuring the SplitView
func (ge *Gide) SplitViewConfig() kit.TypeAndNameList {
	config := kit.TypeAndNameList{}
	config.Add(gi.KiT_Frame, "filetree")
	for i := 0; i < NTextViews; i++ {
		config.Add(gi.KiT_Layout, fmt.Sprintf("textview-%v", i))
	}
	config.Add(gi.KiT_TabView, "main-tabs")
	config.Add(gi.KiT_TabView, "vis-tabs")
	return config
}

var fnFolderProps = ki.Props{
	"icon":     "folder-open",
	"icon-off": "folder",
}

// ConfigSplitView configures the SplitView.
func (ge *Gide) ConfigSplitView() {
	split := ge.SplitView()
	if split == nil {
		return
	}
	split.Dim = gi.X
	//	split.Dim = gi.Y

	config := ge.SplitViewConfig()
	mods, updt := split.ConfigChildren(config, true)
	if mods {
		ftfr := split.KnownChild(FileTreeIdx).(*gi.Frame)
		if !ftfr.HasChildren() {
			ft := ftfr.AddNewChild(KiT_FileTreeView, "filetree").(*FileTreeView)
			ft.SetRootNode(&ge.Files)
			ft.TreeViewSig.Connect(ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
				if data == nil {
					return
				}
				tvn, _ := data.(ki.Ki).Embed(KiT_FileTreeView).(*FileTreeView)
				gee, _ := recv.Embed(KiT_Gide).(*Gide)
				if tvn.SrcNode.Ptr != nil {
					fn := tvn.SrcNode.Ptr.Embed(giv.KiT_FileNode).(*giv.FileNode)
					switch sig {
					case int64(giv.TreeViewSelected):
						gee.FileNodeSelected(fn, tvn)
					case int64(giv.TreeViewOpened):
						gee.FileNodeOpened(fn, tvn)
					case int64(giv.TreeViewClosed):
						gee.FileNodeClosed(fn, tvn)
					}
				}
			})
		}
		for i := 0; i < NTextViews; i++ {
			txly := split.KnownChild(TextView1Idx + i).(*gi.Layout)
			txly.SetStretchMaxWidth()
			txly.SetStretchMaxHeight()
			txly.SetMinPrefWidth(units.NewValue(20, units.Ch))
			txly.SetMinPrefHeight(units.NewValue(10, units.Ch))
			if !txly.HasChildren() {
				ted := txly.AddNewChild(giv.KiT_TextView, fmt.Sprintf("textview-%v", i)).(*giv.TextView)
				ted.TextViewSig.Connect(ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
					gee, _ := recv.Embed(KiT_Gide).(*Gide)
					tee := send.Embed(giv.KiT_TextView).(*giv.TextView)
					gee.TextViewSig(tee, giv.TextViewSignals(sig))
				})
			}
		}

		mtab := split.KnownChild(MainTabsIdx).(*gi.TabView)
		mtab.TabViewSig.Connect(ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
			gee, _ := recv.Embed(KiT_Gide).(*Gide)
			tvsig := gi.TabViewSignals(sig)
			switch tvsig {
			case gi.TabDeleted:
				gee.MainTabDeleted(data.(string))
				if data == "Find" {
					ge.ActiveTextView().ClearHighlights()
				}
			}
		})

		split.SetSplits(ge.Prefs.Splits...)
		split.UpdateEnd(updt)
	}
	for i := 0; i < NTextViews; i++ {
		txly := split.KnownChild(1 + i).(*gi.Layout)
		txed := txly.KnownChild(0).(*giv.TextView)
		if ge.Prefs.Editor.WordWrap {
			txed.SetProp("white-space", gi.WhiteSpacePreWrap)
		} else {
			txed.SetProp("white-space", gi.WhiteSpacePre)
		}
		txed.SetProp("tab-size", ge.Prefs.Editor.TabSize)
		txed.SetProp("font-family", Prefs.FontFamily)
	}

	// set some properties always, even if no mods
	split.SetSplits(ge.Prefs.Splits...)
}

// FileNodeSelected is called whenever tree browser has file node selected
func (ge *Gide) FileNodeSelected(fn *giv.FileNode, tvn *FileTreeView) {
	// if fn.IsDir() {
	// } else {
	// }
}

var GideBigFileSize = 10000000 // 10Mb?

// FileNodeOpened is called whenever file node is double-clicked in file tree
func (ge *Gide) FileNodeOpened(fn *giv.FileNode, tvn *FileTreeView) {
	// todo: could add all these options in LangOpts
	switch fn.Info.Cat {
	case filecat.Folder:
		if !fn.IsOpen() {
			tvn.SetOpen()
			fn.OpenDir()
		}
	case filecat.Exe:
		ge.SetArgVarVals()
		ArgVarVals["{PromptString1}"] = string(fn.FPath)
		CmdNoUserPrompt = true                            // don't re-prompt!
		ge.ExecCmdName(CmdName("Run Prompt"), true, true) // sel, clear
	case filecat.Font:
		fallthrough
	case filecat.Video:
		fallthrough
	case filecat.Audio:
		ge.ExecCmdNameFileNode(fn, CmdName("Open File"), true, true) // sel, clear
	case filecat.Sheet:
		ge.ExecCmdNameFileNode(fn, CmdName("Open File"), true, true) // sel, clear
	case filecat.Bin:
		// todo: prompt??
		ge.ExecCmdNameFileNode(fn, CmdName("Open File"), true, true) // sel, clear
	case filecat.Archive:
		ge.ExecCmdNameFileNode(fn, CmdName("Open File"), true, true) // sel, clear
	case filecat.Image:
		// todo: handle various image types in visualizer natively..
		ge.ExecCmdNameFileNode(fn, CmdName("Open File"), true, true) // sel, clear
	default:
		// program, document, data
		if int(fn.Info.Size) > GideBigFileSize {
			gi.ChoiceDialog(ge.Viewport, gi.DlgOpts{Title: "File is relatively large",
				Prompt: fmt.Sprintf("The file: %v is relatively large at: %v -- really open for editing?", fn.Nm, fn.Info.Size)},
				[]string{"Open", "Cancel"},
				ge.This(), func(recv, send ki.Ki, sig int64, data interface{}) {
					switch sig {
					case 0:
						ge.NextViewFileNode(fn)
					case 1:
						// do nothing
					}
				})
		} else {
			ge.NextViewFileNode(fn)
		}
	}
}

// FileNodeClosed is called whenever file tree browser node is closed
func (ge *Gide) FileNodeClosed(fn *giv.FileNode, tvn *FileTreeView) {
	if fn.IsDir() {
		if fn.IsOpen() {
			fn.CloseDir()
		}
	}
}

func (ge *Gide) GideKeys(kt *key.ChordEvent) {
	var kf KeyFuns
	kc := kt.Chord()
	if gi.KeyEventTrace {
		fmt.Printf("Gide KeyInput: %v\n", ge.PathUnique())
	}
	gkf := gi.KeyFun(kc)
	if ge.KeySeq1 != "" {
		kf = KeyFun(ge.KeySeq1, kc)
		seqstr := string(ge.KeySeq1) + " " + string(kc)
		if kf == KeyFunNil || kc == "Escape" {
			if gi.KeyEventTrace {
				fmt.Printf("gide.KeyFun sequence: %v aborted\n", seqstr)
			}
			ge.SetStatus(seqstr + " -- aborted")
			kt.SetProcessed() // abort key sequence, don't send esc to anyone else
			ge.KeySeq1 = ""
			return
		}
		ge.SetStatus(seqstr)
		ge.KeySeq1 = ""
		gkf = gi.KeyFunNil // override!
	} else {
		kf = KeyFun(kc, "")
		if kf == KeyFunNeeds2 {
			kt.SetProcessed()
			ge.KeySeq1 = kt.Chord()
			ge.SetStatus(string(ge.KeySeq1))
			if gi.KeyEventTrace {
				fmt.Printf("gide.KeyFun sequence needs 2 after: %v\n", ge.KeySeq1)
			}
			return
		} else if kf != KeyFunNil {
			if gi.KeyEventTrace {
				fmt.Printf("gide.KeyFun got in one: %v = %v\n", ge.KeySeq1, kf)
			}
			gkf = gi.KeyFunNil // override!
		}
	}

	switch gkf {
	case gi.KeyFunFind:
		kt.SetProcessed()
		tv := ge.ActiveTextView()
		if tv.HasSelection() {
			ge.Prefs.Find.Find = string(tv.Selection().ToBytes())
		}
		giv.CallMethod(ge, "Find", ge.Viewport)
	}
	if kt.IsProcessed() {
		return
	}
	switch kf {
	case KeyFunNextPanel:
		kt.SetProcessed()
		ge.FocusNextPanel()
	case KeyFunPrevPanel:
		kt.SetProcessed()
		ge.FocusPrevPanel()
	case KeyFunFileOpen:
		kt.SetProcessed()
		giv.CallMethod(ge, "ViewFile", ge.Viewport)
	case KeyFunBufSelect:
		kt.SetProcessed()
		ge.SelectOpenNode()
	case KeyFunBufClone:
		kt.SetProcessed()
		ge.CloneActiveView()
	case KeyFunBufSave:
		kt.SetProcessed()
		ge.SaveActiveView()
	case KeyFunBufSaveAs:
		kt.SetProcessed()
		giv.CallMethod(ge, "SaveActiveViewAs", ge.Viewport)
	case KeyFunBufClose:
		kt.SetProcessed()
		ge.CloseActiveView()
	case KeyFunExecCmd:
		kt.SetProcessed()
		giv.CallMethod(ge, "ExecCmd", ge.Viewport)
	case KeyFunRegCopy:
		kt.SetProcessed()
		giv.CallMethod(ge, "RegisterCopy", ge.Viewport)
	case KeyFunRegPaste:
		kt.SetProcessed()
		giv.CallMethod(ge, "RegisterPaste", ge.Viewport)
	case KeyFunCommentOut:
		kt.SetProcessed()
		ge.CommentOut()
	case KeyFunIndent:
		kt.SetProcessed()
		ge.Indent()
	case KeyFunJump:
		kt.SetProcessed()
		tv := ge.ActiveTextView()
		tv.JumpToLinePrompt()
		ge.Indent()
	case KeyFunSetSplit:
		kt.SetProcessed()
		giv.CallMethod(ge, "SplitsSetView", ge.Viewport)
	case KeyFunBuildProj:
		kt.SetProcessed()
		ge.Build()
	case KeyFunRunProj:
		kt.SetProcessed()
		ge.Run()
	}
}

func (ge *Gide) KeyChordEvent() {
	// need hipri to prevent 2-seq guys from being captured by others
	ge.ConnectEvent(oswin.KeyChordEvent, gi.HiPri, func(recv, send ki.Ki, sig int64, d interface{}) {
		gee := recv.Embed(KiT_Gide).(*Gide)
		kt := d.(*key.ChordEvent)
		gee.GideKeys(kt)
	})
}

func (ge *Gide) Render2D() {
	ge.ToolBar().UpdateActions()
	if win := ge.ParentWindow(); win != nil {
		sv := ge.SplitView()
		if sv != nil {
			win.SetStartFocus(sv.This())
		}
		if !win.IsResizing() {
			win.MainMenuUpdateActives()
		}
	}
	ge.Frame.Render2D()
}

func (ge *Gide) ConnectEvents2D() {
	if ge.HasAnyScroll() {
		ge.LayoutScrollEvents()
	}
	ge.KeyChordEvent()
}

// GideInactiveEmptyFunc is an ActionUpdateFunc that inactivates action if project is empty
var GideInactiveEmptyFunc = giv.ActionUpdateFunc(func(gei interface{}, act *gi.Action) {
	ge := gei.(ki.Ki).Embed(KiT_Gide).(*Gide)
	act.SetInactiveState(ge.IsEmpty())
})

var GideProps = ki.Props{
	"background-color": &gi.Prefs.Colors.Background,
	"color":            &gi.Prefs.Colors.Font,
	"max-width":        -1,
	"max-height":       -1,
	"#title": ki.Props{
		"max-width":        -1,
		"horizontal-align": gi.AlignCenter,
		"vertical-align":   gi.AlignTop,
	},
	"MethViewNoUpdateAfter": true, // no update after is default for everything
	"ToolBar": ki.PropSlice{
		{"UpdateFiles", ki.Props{
			"shortcut": "Command+U",
			"desc":     "update file browser list of files",
			"icon":     "update",
		}},
		{"ViewFile", ki.Props{
			"label": "Open",
			"icon":  "file-open",
			"desc":  "open a file in current active text view",
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunFileOpen).String())
			}),
			"Args": ki.PropSlice{
				{"File Name", ki.Props{
					"default-field": "ActiveFilename",
				}},
			},
		}},
		{"SaveActiveView", ki.Props{
			"label": "Save",
			"desc":  "save active text view file to its current filename",
			"icon":  "file-save",
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunBufSave).String())
			}),
		}},
		{"SaveActiveViewAs", ki.Props{
			"label": "Save As...",
			"icon":  "file-save",
			"desc":  "save active text view file to a new filename",
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunBufSaveAs).String())
			}),
			"Args": ki.PropSlice{
				{"File Name", ki.Props{
					"default-field": "ActiveFilename",
				}},
			},
		}},
		{"ViewOpenNodeName", ki.Props{
			"icon":         "file-text",
			"label":        "Edit",
			"desc":         "select an open file to view in active text view",
			"submenu-func": giv.SubMenuFunc(GideOpenNodes),
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunBufSelect).String())
			}),
			"Args": ki.PropSlice{
				{"Node Name", ki.Props{}},
			},
		}},
		{"sep-find", ki.BlankProp{}},
		{"CursorToHistPrev", ki.Props{
			"icon":     "widget-wedge-left",
			"shortcut": gi.KeyFunHistPrev,
			"label":    "",
			"desc":     "move cursor to previous location in active text view",
		}},
		{"CursorToHistNext", ki.Props{
			"icon":     "widget-wedge-right",
			"shortcut": gi.KeyFunHistNext,
			"label":    "",
			"desc":     "move cursor to next location in active text view",
		}},
		{"Find", ki.Props{
			"label":    "Find...",
			"icon":     "search",
			"desc":     "Find / replace in all open folders in file browser",
			"shortcut": gi.KeyFunFind,
			"Args": ki.PropSlice{
				{"Search For", ki.Props{
					"default-field": "Prefs.Find.Find",
					"history-field": "Prefs.Find.FindHist",
					"width":         80,
				}},
				{"Replace With", ki.Props{
					"desc":          "Optional replace string -- replace will be controlled interactively in Find panel, including replace all",
					"default-field": "Prefs.Find.Replace",
					"history-field": "Prefs.Find.ReplHist",
					"width":         80,
				}},
				{"Ignore Case", ki.Props{
					"default-field": "Prefs.Find.IgnoreCase",
				}},
				{"Location", ki.Props{
					"desc":          "location to find in",
					"default-field": "Prefs.Find.Loc",
				}},
				{"Languages", ki.Props{
					"desc":          "restrict find to files associated with these languages -- leave empty for all files",
					"default-field": "Prefs.Find.Langs",
				}},
			},
		}},
		{"Spell", ki.Props{
			"label": "Spelling...",
			"icon":  "spelling",
		}},
		{"sep-file", ki.BlankProp{}},
		{"Build", ki.Props{
			"icon":    "terminal",
			"tooltip": "build the project -- command(s) specified in Project Prefs",
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunBuildProj).String())
			}),
		}},
		{"Run", ki.Props{
			"icon":    "terminal",
			"tooltip": "run the project -- command(s) specified in Project Prefs",
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunRunProj).String())
			}),
		}},
		{"Commit", ki.Props{
			"icon": "star",
		}},
		{"ExecCmdNameActive", ki.Props{
			"icon":         "terminal",
			"label":        "Exec Cmd",
			"desc":         "execute given command on active file / directory / project",
			"submenu-func": giv.SubMenuFunc(GideExecCmds),
			"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
				return key.Chord(ChordForFun(KeyFunExecCmd).String())
			}),
			"Args": ki.PropSlice{
				{"Cmd Name", ki.Props{}},
			},
		}},
		{"sep-splt", ki.BlankProp{}},
		{"Splits", ki.PropSlice{
			{"SplitsSetView", ki.Props{
				"label":   "Set View",
				"submenu": &AvailSplitNames,
				"Args": ki.PropSlice{
					{"Split Name", ki.Props{
						"default-field": "Prefs.SplitName",
					}},
				},
			}},
			{"SplitsSaveAs", ki.Props{
				"label": "Save As...",
				"desc":  "save current splitter values to a new named split configuration",
				"Args": ki.PropSlice{
					{"Name", ki.Props{
						"width": 60,
					}},
					{"Desc", ki.Props{
						"width": 60,
					}},
				},
			}},
			{"SplitsSave", ki.Props{
				"label":   "Save",
				"submenu": &AvailSplitNames,
				"Args": ki.PropSlice{
					{"Split Name", ki.Props{
						"default-field": "Prefs.SplitName",
					}},
				},
			}},
			{"SplitsEdit", ki.Props{
				"label": "Edit...",
			}},
		}},
	},
	"MainMenu": ki.PropSlice{
		{"AppMenu", ki.BlankProp{}},
		{"File", ki.PropSlice{
			{"OpenRecent", ki.Props{
				"submenu": &SavedPaths,
				"Args": ki.PropSlice{
					{"File Name", ki.Props{}},
				},
			}},
			{"OpenProj", ki.Props{
				"shortcut": "Command+O",
				"label":    "Open Project...",
				"desc":     "open a gide project -- can be a .gide file or just a file or directory (projects are just directories with relevant files)",
				"Args": ki.PropSlice{
					{"File Name", ki.Props{
						"default-field": "ProjFilename",
						"ext":           ".gide",
					}},
				},
			}},
			{"OpenPath", ki.Props{
				"label": "Open Path...",
				"desc":  "open a gide project for a file or directory (projects are just directories with relevant files)",
				"Args": ki.PropSlice{
					{"Path", ki.Props{}},
				},
			}},
			{"New", ki.PropSlice{
				{"NewProj", ki.Props{
					"shortcut": "Command+N",
					"label":    "New Project...",
					"desc":     "Create a new project -- select a path for the parent folder, and a folder name for the new project -- all Gide projects are basically folders with files.  You can also specify the main language and {version control system for the project.  For other options, do <code>Proj Prefs</code> in the File menu of the new project.",
					"Args": ki.PropSlice{
						{"Parent Folder", ki.Props{
							"dirs-only":     true, // todo: support
							"default-field": "ProjRoot",
						}},
						{"Folder", ki.Props{
							"width": 60,
						}},
						{"Main Lang", ki.Props{}},
						{"Version Ctrl", ki.Props{}},
					},
				}},
				{"NewFile", ki.Props{
					"label": "New File...",
					"desc":  "Create a new file in project -- to create in sub-folders, use context menu on folder in file browser",
					"Args": ki.PropSlice{
						{"File Name", ki.Props{
							"width": 60,
						}},
					},
				}},
			}},
			{"SaveProj", ki.Props{
				// "shortcut": "Command+S",
				"label":    "Save Project",
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"SaveProjAs", ki.Props{
				// "shortcut": "Shift+Command+S",
				"label":    "Save Project As...",
				"desc":     "Save project to given file name -- this is the .gide file containing preferences and current settings -- also saves all open files -- once saved, further saving is automatic",
				"updtfunc": GideInactiveEmptyFunc,
				"Args": ki.PropSlice{
					{"File Name", ki.Props{
						"default-field": "ProjFilename",
						"ext":           ".gide",
					}},
					{"SaveAll", ki.Props{
						"value": false,
					}},
				},
			}},
			{"sep-af", ki.BlankProp{}},
			{"ViewFile", ki.Props{
				"label": "Open File...",
				"shortcut-func": func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunFileOpen).String())
				},
				"updtfunc": GideInactiveEmptyFunc,
				"Args": ki.PropSlice{
					{"File Name", ki.Props{}},
				},
			}},
			{"SaveActiveView", ki.Props{
				"label": "Save File",
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunBufSave).String())
				}),
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"SaveActiveViewAs", ki.Props{
				"label":    "Save File As...",
				"updtfunc": GideInactiveEmptyFunc,
				"desc":     "save active text view file to a new filename",
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunBufSaveAs).String())
				}),
				"Args": ki.PropSlice{
					{"File Name", ki.Props{
						"default-field": "ActiveFilename",
					}},
				},
			}},
			{"RevertActiveView", ki.Props{
				"desc":     "Revert active file to last saved version: this will lose all active changes -- are you sure?",
				"confirm":  true,
				"label":    "Revert File...",
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"CloseActiveView", ki.Props{
				"label":    "Close File",
				"updtfunc": GideInactiveEmptyFunc,
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunBufClose).String())
				}),
			}},
			{"sep-prefs", ki.BlankProp{}},
			{"ProjPrefs", ki.Props{
				"label":    "Project Prefs...",
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"sep-close", ki.BlankProp{}},
			{"Close Window", ki.BlankProp{}},
		}},
		{"Edit", ki.PropSlice{
			{"Copy", ki.Props{
				"keyfun": gi.KeyFunCopy,
			}},
			{"Cut", ki.Props{
				"keyfun": gi.KeyFunCut,
			}},
			{"Paste", ki.Props{
				"keyfun": gi.KeyFunPaste,
			}},
			{"Paste History...", ki.Props{
				"keyfun": gi.KeyFunPasteHist,
			}},
			{"Registers", ki.PropSlice{
				{"RegisterCopy", ki.Props{
					"label": "Copy...",
					"desc":  "save currently-selected text to a named register, which can be pasted later -- persistent across sessions as well",
					"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
						return key.Chord(ChordForFun(KeyFunRegCopy).String())
					}),
					"updtfunc": GideInactiveEmptyFunc,
					"Args": ki.PropSlice{
						{"Register Name", ki.Props{}},
					},
				}},
				{"RegisterPaste", ki.Props{
					"label": "Paste...",
					"desc":  "paste text from named register",
					"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
						return key.Chord(ChordForFun(KeyFunRegPaste).String())
					}),
					"updtfunc": GideInactiveEmptyFunc,
					"Args": ki.PropSlice{
						{"Register Name", ki.Props{
							"default-field": "Prefs.Register",
						}},
					},
				}},
			}},
			{"sep-undo", ki.BlankProp{}},
			{"Undo", ki.Props{
				"keyfun": gi.KeyFunUndo,
			}},
			{"Redo", ki.Props{
				"keyfun": gi.KeyFunRedo,
			}},
			{"sep-find", ki.BlankProp{}},
			{"Find", ki.Props{
				"label":    "Find...",
				"shortcut": gi.KeyFunFind,
				"desc":     "Find / replace in all open folders in file browser",
				"updtfunc": GideInactiveEmptyFunc,
				"Args": ki.PropSlice{
					{"Search For", ki.Props{
						"default-field": "Prefs.Find.Find",
						"history-field": "Prefs.Find.FindHist",
						"width":         80,
					}},
					{"Replace With", ki.Props{
						"desc":          "Optional replace string -- replace will be controlled interactively in Find panel, including replace all",
						"default-field": "Prefs.Find.Replace",
						"history-field": "Prefs.Find.ReplHist",
						"width":         80,
					}},
					{"Ignore Case", ki.Props{
						"default-field": "Prefs.Find.IgnoreCase",
					}},
					{"Location", ki.Props{
						"desc":          "location to find in",
						"default-field": "Prefs.Find.Loc",
					}},
					{"Languages", ki.Props{
						"desc":          "restrict find to files associated with these languages -- leave empty for all files",
						"default-field": "Prefs.Find.Langs",
					}},
				},
			}},
			{"ReplaceInActive", ki.Props{
				"label":    "Replace In Active...",
				"shortcut": gi.KeyFunReplace,
				"desc":     "query-replace in current active text view only (use Find for multi-file)",
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"Spell", ki.Props{
				"label":    "Spelling...",
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"ShowCompletions", ki.Props{
				"keyfun":   gi.KeyFunComplete,
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"sep-adv", ki.BlankProp{}},
			{"CommentOut", ki.Props{
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunCommentOut).String())
				}),
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"Indent", ki.Props{
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunIndent).String())
				}),
				"updtfunc": GideInactiveEmptyFunc,
			}},
		}},
		{"View", ki.PropSlice{
			{"Panels", ki.PropSlice{
				{"FocusNextPanel", ki.Props{
					"label": "Focus Next",
					"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
						return key.Chord(ChordForFun(KeyFunNextPanel).String())
					}),
					"updtfunc": GideInactiveEmptyFunc,
				}},
				{"FocusPrevPanel", ki.Props{
					"label": "Focus Prev",
					"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
						return key.Chord(ChordForFun(KeyFunPrevPanel).String())
					}),
					"updtfunc": GideInactiveEmptyFunc,
				}},
				{"CloneActiveView", ki.Props{
					"label": "Clone Active",
					"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
						return key.Chord(ChordForFun(KeyFunBufClone).String())
					}),
					"updtfunc": GideInactiveEmptyFunc,
				}},
			}},
			{"Splits", ki.PropSlice{
				{"SplitsSetView", ki.Props{
					"label":    "Set View",
					"submenu":  &AvailSplitNames,
					"updtfunc": GideInactiveEmptyFunc,
					"Args": ki.PropSlice{
						{"Split Name", ki.Props{}},
					},
				}},
				{"SplitsSaveAs", ki.Props{
					"label":    "Save As...",
					"desc":     "save current splitter values to a new named split configuration",
					"updtfunc": GideInactiveEmptyFunc,
					"Args": ki.PropSlice{
						{"Name", ki.Props{
							"width": 60,
						}},
						{"Desc", ki.Props{
							"width": 60,
						}},
					},
				}},
				{"SplitsSave", ki.Props{
					"label":    "Save",
					"submenu":  &AvailSplitNames,
					"updtfunc": GideInactiveEmptyFunc,
					"Args": ki.PropSlice{
						{"Split Name", ki.Props{}},
					},
				}},
				{"SplitsEdit", ki.Props{
					"updtfunc": GideInactiveEmptyFunc,
					"label":    "Edit...",
				}},
			}},
			{"OpenConsoleTab", ki.Props{
				"updtfunc": GideInactiveEmptyFunc,
			}},
		}},
		{"Navigate", ki.PropSlice{
			{"Cursor", ki.PropSlice{
				{"Back", ki.Props{
					"keyfun": gi.KeyFunHistPrev,
				}},
				{"Forward", ki.Props{
					"keyfun": gi.KeyFunHistNext,
				}},
				{"Jump To Line", ki.Props{
					"keyfun": gi.KeyFunJump,
				}},
			}},
		}},
		{"Command", ki.PropSlice{
			{"Build", ki.Props{
				"updtfunc": GideInactiveEmptyFunc,
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunBuildProj).String())
				}),
			}},
			{"Run", ki.Props{
				"updtfunc": GideInactiveEmptyFunc,
				"shortcut-func": giv.ShortcutFunc(func(gei interface{}, act *gi.Action) key.Chord {
					return key.Chord(ChordForFun(KeyFunRunProj).String())
				}),
			}},
			{"Commit", ki.Props{
				"updtfunc": GideInactiveEmptyFunc,
			}},
			{"ExecCmdNameActive", ki.Props{
				"label":        "Exec Cmd",
				"submenu-func": giv.SubMenuFunc(GideExecCmds),
				"updtfunc":     GideInactiveEmptyFunc,
				"Args": ki.PropSlice{
					{"Cmd Name", ki.Props{}},
				},
			}},
			{"DiffFiles", ki.Props{
				"updtfunc": GideInactiveEmptyFunc,
				"Args": ki.PropSlice{
					{"File Name 1", ki.Props{}},
					{"File Name 2", ki.Props{}},
				},
			}},
		}},
		{"Window", "Windows"},
		{"Help", ki.PropSlice{
			{"HelpWiki", ki.Props{}},
		}},
	},
	"CallMethods": ki.PropSlice{
		{"NextViewFile", ki.Props{
			"Args": ki.PropSlice{
				{"File Name", ki.Props{
					"default-field": "ActiveFilename",
				}},
			},
		}},
		{"SplitsSetView", ki.Props{
			"Args": ki.PropSlice{
				{"Split Name", ki.Props{}},
			},
		}},
		{"ExecCmd", ki.Props{}},
	},
}

//////////////////////////////////////////////////////////////////////////////////////
//   Project window

func init() {
	gi.CustomAppMenuFunc = func(m *gi.Menu, win *gi.Window) {
		m.InsertActionAfter("GoGi Preferences...", gi.ActOpts{Label: "Gide Preferences..."},
			win, func(recv, send ki.Ki, sig int64, data interface{}) {
				PrefsView(&Prefs)
			})
	}
}

// NewGideProjPath creates a new Gide window with a new Gide project for given
// path, returning the window and the path
func NewGideProjPath(path string) (*gi.Window, *Gide) {
	_, projnm, _, _ := ProjPathParse(path)
	return NewGideWindow(path, projnm, true)
}

// OpenGideProj creates a new Gide window opened to given Gide project,
// returning the window and the path
func OpenGideProj(projfile string) (*gi.Window, *Gide) {
	pp := &ProjPrefs{}
	if err := pp.OpenJSON(gi.FileName(projfile)); err != nil {
		gi.PromptDialog(nil, gi.DlgOpts{Title: "Project File Could Not Be Opened", Prompt: fmt.Sprintf("Project file open encountered error: %v", err.Error())}, true, false, nil, nil)
		return nil, nil
	}
	path := string(pp.ProjRoot)
	_, projnm, _, _ := ProjPathParse(path)
	return NewGideWindow(projfile, projnm, false)
}

// NewGideWindow is common code for Open GideWindow from Proj or Path
func NewGideWindow(path, projnm string, doPath bool) (*gi.Window, *Gide) {
	winm := "gide-" + projnm

	if win, found := gi.AllWindows.FindName(winm); found {
		mfr := win.SetMainFrame()
		ge := mfr.KnownChild(0).Embed(KiT_Gide).(*Gide)
		win.OSWin.Raise()
		return win, ge
	}

	width := 1280
	height := 720

	win := gi.NewWindow2D(winm, winm, width, height, true) // true = pixel sizes

	vp := win.WinViewport2D()
	updt := vp.UpdateStart()

	mfr := win.SetMainFrame()
	ge := mfr.AddNewChild(KiT_Gide, "gide").(*Gide)
	ge.Viewport = vp

	if doPath {
		ge.OpenPath(gi.FileName(path))
	} else {
		ge.OpenProj(gi.FileName(path))
	}

	mmen := win.MainMenu
	giv.MainMenuView(ge, win, mmen)

	inClosePrompt := false
	win.OSWin.SetCloseReqFunc(func(w oswin.Window) {
		if !inClosePrompt {
			inClosePrompt = true
			if ge.CloseWindowReq() {
				win.Close()
			} else {
				inClosePrompt = false
			}
		}
	})

	// win.OSWin.SetCloseCleanFunc(func(w oswin.Window) {
	// 	fmt.Printf("Doing final Close cleanup here..\n")
	// })

	win.OSWin.SetCloseCleanFunc(func(w oswin.Window) {
		if gi.MainWindows.Len() <= 1 {
			go oswin.TheApp.Quit() // once main window is closed, quit
		}
	})

	win.MainMenuUpdated()

	vp.UpdateEndNoSig(updt)

	win.GoStartEventLoop()

	cmd := exec.Command("gocode", "close")
	defer cmd.Run()

	return win, ge
}
