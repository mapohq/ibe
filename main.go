package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"path"
	"runtime"
	"time"

	"ibe/version"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/explorer"

	"github.com/dunhamsteve/ios/backup"
)

func main() {
	go func() {
		w := app.NewWindow(app.Size(unit.Dp(1024), unit.Dp(768)), app.Title("iOS Backup Exporter"))
		err := run(w)
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

type (
	D = layout.Dimensions
	C = layout.Context
)

type backupState struct {
	loading bool
	err     error
	backups []*backupItem

	list            widget.List
	domainEditor    widget.Editor
	setDomainButton widget.Clickable
}

const domainPrefix = "AppDomain-"

var domain = "com.tencent.xin"

func (b *backupState) Layout(gtx C, th *material.Theme) D {
	R := layout.Rigid

	if b.setDomainButton.Clicked() {
		domain = b.domainEditor.Text()
		for _, bi := range b.backups {
			if bi.loaded {
				bi.files, bi.totalSize = stats(bi.DB, domain)
			}
		}
	}

	title := material.H3(th, "iOS Backup Exporter")
	title.Color = color.NRGBA{R: 127, G: 0, B: 0, A: 255}
	title.Font.Weight = text.Bold
	title.Alignment = text.Middle

	version := material.H5(th, fmt.Sprintf("version: %s", version.BuildVersion()))
	version.Alignment = text.Middle

	f := func(gtx C) D {
		border := widget.Border{Color: color.NRGBA{A: 0xff}, CornerRadius: unit.Dp(8), Width: unit.Dp(2)}
		de := material.Editor(th, &b.domainEditor, domain)
		dom := func(gtx C) D {
			if b.loading {
				gtx = gtx.Disabled()
			}
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(200))
			return border.Layout(gtx, func(gtx C) D {
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, de.Layout)
			})
		}

		btn := func(gtx C) D {
			if b.loading || b.domainEditor.Text() == "" || b.domainEditor.Text() == domain {
				gtx = gtx.Disabled()
			}
			return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, material.Button(th, &b.setDomainButton, "Change App").Layout)
		}

		children := []layout.FlexChild{R(dom), R(btn)}
		return layout.Inset{Left: unit.Dp(16), Top: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
		})
	}

	widgets := []layout.Widget{
		title.Layout,
		version.Layout,
		f,
	}

	if b.err != nil {
		widgets = append(widgets, errorLabel(th, b.err))
	} else if b.loading {
		t := func(gtx C) D {
			gtx.Constraints.Max.Y = gtx.Dp(unit.Dp(40))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(40))
			return material.Loader(th).Layout(gtx)
		}
		widgets = append(widgets, t)
	} else {
		if len(b.backups) == 0 {
			t := material.H5(th, "No Backups Found")
			t.Alignment = text.Middle
			widgets = append(widgets, t.Layout)
		} else {
			for _, bb := range b.backups {
				b := bb
				widgets = append(widgets, b.Layout)
			}
		}
	}

	return material.List(th, &b.list).Layout(gtx, len(widgets), func(gtx C, i int) D {
		if i < 3 {
			return widgets[i](gtx)
		}

		return layout.UniformInset(unit.Dp(16)).Layout(gtx, widgets[i])
	})
}

type backupItem struct {
	Backup      backup.Backup
	DB          *backup.MobileBackup
	IsEncrypted bool

	files     int
	totalSize uint64

	passwordError error
	saving        bool
	savingPercent int
	saved         bool
	savingError   error

	loaded       bool
	loading      bool
	loadingError error

	updateChan     chan func()
	exportBtn      widget.Clickable
	setPasswordBtn widget.Clickable
	passwordEditor *widget.Editor
	expl           *explorer.Explorer
	th             *material.Theme
}

func (b *backupItem) Layout(gtx C) D {
	th := b.th
	expl := b.expl
	R := layout.Rigid

	if b.loaded && b.exportBtn.Clicked() {
		name := b.Backup.FileName
		if len(name) > 8 {
			name = name[:8]
		}
		name += "-" + time.Now().Format("20060102")
		fname := name + ".zip"
		go func() {
			file, err := expl.CreateFile(fname)
			if err != nil {
				log.Printf("Failed to create file: %v\n", err)
				return
			}

			b.updateChan <- func() {
				b.savingError = nil
				b.saved = false
				b.saving = true
			}

			err = b.zipApp(domain, file, name)

			b.updateChan <- func() {
				if err != nil {
					b.savingError = err
				} else {
					b.saving = false
					b.saved = true
				}
			}

			defer func() {
				file.Close()
			}()
		}()
	}

	if b.setPasswordBtn.Clicked() {
		b.passwordError = nil
		pass := b.passwordEditor.Text()
		if pass != "" {
			if err := b.DB.SetPassword(pass); err != nil {
				b.passwordError = err
			} else {
				go func() {
					b.updateChan <- func() {
						b.loading = true
						b.loadingError = nil
						b.loaded = false
					}

					err := b.DB.Load()
					b.updateChan <- func() {
						if err != nil {
							b.loadingError = err
						} else {
							b.loading = false
							b.loaded = true
							b.files, b.totalSize = stats(b.DB, domain)
						}
					}
				}()
			}
		}
	}

	name := func(gtx C) D {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, material.H5(th, fmt.Sprintf("%s - %s", b.Backup.DeviceName, b.Backup.FileName)).Layout)
	}
	children := []layout.FlexChild{
		R(name),
	}

	if b.loadingError != nil {
		children = append(children, R(errorLabel(th, b.loadingError)))
	} else if b.loaded {
		f := func(gtx C) D {
			var children []layout.FlexChild
			if b.files > 0 {
				info := material.Body1(th, fmt.Sprintf("%d files %.02f GB", b.files, math.Round(float64(b.totalSize)*100/(1024*1024*1024))/100))
				info.Font.Style = text.Italic
				btn := func(gtx C) D {
					bt := material.Button(th, &b.exportBtn, "Export")
					if b.saving {
						gtx = gtx.Disabled()
					}
					return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, bt.Layout)
				}
				children = []layout.FlexChild{R(info.Layout), R(btn)}
				if b.saving {
					children = append(children, R(textLabel(th, fmt.Sprintf("saving %d%%", b.savingPercent))))
				} else if b.saved {
					children = append(children, R(textLabel(th, "saved")))
				} else if b.savingError != nil {
					children = append(children, R(errorLabel(th, b.savingError)))
				}
			} else {
				children = []layout.FlexChild{R(errorLabel(th, errors.New("no app")))}
			}
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
		}
		children = append(children, R(f))
	} else if b.IsEncrypted {
		f := func(gtx C) D {
			border := widget.Border{Color: color.NRGBA{A: 0xff}, CornerRadius: unit.Dp(8), Width: unit.Dp(2)}
			pe := material.Editor(th, b.passwordEditor, "Password")
			pass := func(gtx C) D {
				if b.loading || b.loaded {
					gtx = gtx.Disabled()
				}
				gtx.Constraints.Min.X = gtx.Dp(unit.Dp(200))
				return border.Layout(gtx, func(gtx C) D {
					return layout.UniformInset(unit.Dp(8)).Layout(gtx, pe.Layout)
				})
			}

			btn := func(gtx C) D {
				if b.loading || b.loaded || b.passwordEditor.Text() == "" {
					gtx = gtx.Disabled()
				}
				return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, material.Button(th, &b.setPasswordBtn, "Set Password").Layout)
			}

			children = []layout.FlexChild{R(pass), R(btn)}
			if b.loading {
				children = append(children, R(textLabel(th, "loading")))
			}
			if b.passwordError != nil {
				children = append(children, R(errorLabel(th, b.passwordError)))
			}
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
		}
		children = append(children, R(f))
	}

	border := widget.Border{Color: color.NRGBA{R: 0xe0, G: 0xe0, B: 0xe0, A: 0xff}, CornerRadius: unit.Dp(4), Width: unit.Dp(2)}
	return border.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(unit.Dp(8)).Layout(gtx,
			func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
	})
}

func (bi *backupItem) zipApp(_domain string, zipFile io.WriteCloser, name string) (err error) {
	var total int64
	zf := zip.NewWriter(zipFile)

	domain := domainPrefix + _domain
	db := bi.DB
	var count int
	for _, rec := range db.Records {
		if rec.Domain != domain {
			continue
		}
		if rec.Length == 0 {
			continue
		}

		r, err := db.FileReader(rec)
		if err != nil {
			log.Printf("error reading file: %s: %v", rec.Path, err)
			continue
		}

		header := &zip.FileHeader{
			Name:   path.Join(name, rec.Path),
			Method: zip.Store,
		}

		w, err := zf.CreateHeader(header)
		if err != nil {
			//				log.Printf("Failed to create header %s\n", p)
			return err
		}

		n, _ := io.Copy(w, r)
		total += n
		count++
		r.Close()
		if count%500 == 0 {
			bi.updateChan <- func() {
				bi.savingPercent = count * 100 / bi.files
			}
		}
	}

	if err = zf.Close(); err != nil {
		log.Printf("Failed to close zipfile:%v\n", err)
		return err
	}

	log.Printf("Wrote %d bytes\n", total)
	return nil
}

func stats(db *backup.MobileBackup, _domain string) (files int, size uint64) {
	domain := domainPrefix + _domain
	for _, rec := range db.Records {
		if rec.Domain != domain {
			continue
		}
		files++
		size += rec.Length
	}
	return
}

func run(w *app.Window) error {
	updateChan := make(chan func())
	th := setupTheme()
	expl := explorer.NewExplorer(w)
	bak := backupState{
		loading: true,
		list: widget.List{
			List: layout.List{
				Axis: layout.Vertical,
			},
		},
		domainEditor: widget.Editor{
			SingleLine: true,
		},
	}
	bak.domainEditor.SetText(domain)

	// init backups
	go func() {
		backups, err := backup.Enumerate()
		if err != nil {
			updateChan <- func() {
				bak.loading = false
				bak.err = err
			}
			return
		}

		var bis []*backupItem
		for _, b := range backups {
			bi := &backupItem{Backup: b, updateChan: updateChan}
			bis = append(bis, bi)

			db, err := backup.Open(b.FileName)
			if err != nil {
				bi.loadingError = err
				continue
			}

			bi.DB = db
			if db.Manifest.IsEncrypted {
				bi.IsEncrypted = true
				continue
			}

			if err := db.Load(); err != nil {
				bi.loadingError = err
				continue
			}

			bi.files, bi.totalSize = stats(db, domain)
			bi.loaded = true
		}

		updateChan <- func() {
			bak.backups = bis
			bak.loading = false

			for _, bi := range bak.backups {
				bi.expl = expl
				bi.th = th
				if bi.IsEncrypted {
					bi.passwordEditor = &widget.Editor{
						SingleLine: true,
						Submit:     true,
					}
				}
			}
		}
	}()

	var ops op.Ops
	for {
		select {
		case cb := <-updateChan:
			if cb != nil {
				cb()
			}
			w.Invalidate()

		case e := <-w.Events():
			switch e := e.(type) {
			case system.DestroyEvent:
				return e.Err
			case system.FrameEvent:
				gtx := layout.NewContext(&ops, e)
				bak.Layout(gtx, th)
				e.Frame(gtx.Ops)
			}
		}
	}
}

func setupTheme() *material.Theme {
	var c []text.FontFace
	if runtime.GOOS == "darwin" {
		if bs, err := os.ReadFile("/Library/Fonts/Arial Unicode.ttf"); err == nil {
			if fnt, err := opentype.Parse(bs); err == nil {
				c = append(c, text.FontFace{Face: fnt}, text.FontFace{Font: text.Font{Style: text.Italic}, Face: fnt})
			}
		}
	} else if runtime.GOOS == "windows" {
		if bs, err := os.ReadFile("C:\\Windows\\Fonts\\DENG.TTF"); err == nil {
			if fnt, err := opentype.Parse(bs); err == nil {
				c = append(c, text.FontFace{Face: fnt})
			}
		}
	}

	if len(c) == 0 {
		c = gofont.Collection()
	}

	return material.NewTheme(c)
}

func errorLabel(th *material.Theme, err error) layout.Widget {
	t := material.Body1(th, fmt.Sprintf("error: %s", err.Error()))
	t.Color = color.NRGBA{R: 0xff, A: 0xff}
	return func(gtx C) D {
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, t.Layout)
	}
}

func textLabel(th *material.Theme, txt string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, material.Body1(th, txt).Layout)
	}
}
