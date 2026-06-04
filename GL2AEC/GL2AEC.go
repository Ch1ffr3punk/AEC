package main

import (
	"bytes"
	"image/color"
	"image/png"
	"net/url"
	"regexp"
	"strings"

	"github.com/skip2/go-qrcode"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type purpleTheme struct {
	base fyne.Theme
}

func (p *purpleTheme) Font(s fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(s)
}

func (p *purpleTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 122, G: 110, B: 243, A: 255}
	case theme.ColorNameHyperlink:
		return color.NRGBA{R: 122, G: 110, B: 243, A: 255}
	case theme.ColorNameButton:
		return color.NRGBA{R: 122, G: 110, B: 243, A: 255}
	default:
		if p.base != nil {
			return p.base.Color(name, variant)
		}
		return theme.DefaultTheme().Color(name, variant)
	}
}

func (p *purpleTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	if p.base != nil {
		return p.base.Icon(name)
	}
	return theme.DefaultTheme().Icon(name)
}

func (p *purpleTheme) Size(name fyne.ThemeSizeName) float32 {
	if p.base != nil {
		return p.base.Size(name)
	}
	return theme.DefaultTheme().Size(name)
}

type App struct {
	app       fyne.App
	window    fyne.Window
	status    *widget.Label
	textEntry *widget.Entry
	qrImage   *canvas.Image
	dark      bool
	themeBtn  *widget.Button
	infoBtn   *widget.Button
}

func (a *App) toggleTheme() {
	if a.dark {
		a.app.Settings().SetTheme(&purpleTheme{base: theme.LightTheme()})
		a.themeBtn.SetText("🌙")
	} else {
		a.app.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})
		a.themeBtn.SetText("☀️")
	}
	a.dark = !a.dark
}

func (a *App) showInfo() {
	projURL, _ := url.Parse("https://github.com/Ch1ffr3punk/AEC")
	projectLink := widget.NewHyperlink("An Open Source project", projURL)
	okButton := widget.NewButton("OK", func() {
		if overlays := a.window.Canvas().Overlays(); overlays.Top() != nil {
			overlays.Remove(overlays.Top())
		}
	})
	okButton.Importance = widget.HighImportance
	content := container.NewVBox(
		widget.NewLabelWithStyle("GL2AEC v0.1.0", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		container.NewHBox(layout.NewSpacer(), projectLink, layout.NewSpacer()),
		widget.NewLabelWithStyle("released under the Apache 2.0 license", fyne.TextAlignCenter, fyne.TextStyle{}),
		widget.NewLabelWithStyle("© 2026 Ch1ffr3punk", fyne.TextAlignCenter, fyne.TextStyle{}),
		container.NewHBox(layout.NewSpacer(), okButton, layout.NewSpacer()),
	)
	dialog.ShowCustomWithoutButtons("", content, a.window)
}

func (a *App) onTextChanged() {
	text := strings.TrimSpace(a.textEntry.Text)

	artifacts := []string{
		" doesn’t support a secure connection",
		" doesn't support a secure connection",
		"Attackers can see and change information",
		"It's safest to visit this site later",
		"You might also contact the site owner",
		"Learn more about this warning",
	}

	for _, artifact := range artifacts {
		if idx := strings.Index(text, artifact); idx != -1 {
			text = strings.TrimSpace(text[:idx])
			break
		}
	}

	text = strings.TrimSpace(text)

	if len(text) == 0 {
		a.qrImage.Image = nil
		a.qrImage.Refresh()
		a.status.SetText("Ready")
		return
	}

	if len(text) == 768 {
		text = strings.ToUpper(text)
		if text != strings.TrimSpace(a.textEntry.Text) {
			a.textEntry.SetText(text)
		}
	}

	isValidHexKey, _ := regexp.MatchString(`^[a-fA-F0-9]{64}$`, text)
	isValidArmored, _ := regexp.MatchString(`^[A-Z]{768}$`, text)

	if !isValidHexKey && !isValidArmored {
		a.qrImage.Image = nil
		a.qrImage.Refresh()
		a.status.SetText("Invalid: Need 64 hex chars or 768 A-Z chars")
		return
	}

	qr, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		a.status.SetText(err.Error())
		return
	}
	qr.DisableBorder = false
	img := qr.Image(512)
	
	a.qrImage.Image = img
	a.qrImage.Refresh()

	if isValidHexKey {
		a.status.SetText("Valid Public Key")
	} else {
		a.status.SetText("Valid Encrypted Data")
	}
}

func (a *App) saveQR() {
	if a.qrImage.Image == nil {
		dialog.ShowInformation("Error", "No QR code to save", a.window)
		return
	}
	dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
		if writer != nil {
			defer writer.Close()
			var buf bytes.Buffer
			png.Encode(&buf, a.qrImage.Image)
			writer.Write(buf.Bytes())
			a.status.SetText("saved")
		}
	}, a.window)
}

func (a *App) pasteFromClipboard() {
	if a.window.Clipboard() == nil {
		a.status.SetText("Clipboard not available")
		return
	}
	text := a.window.Clipboard().Content()
	if text != "" {
		a.textEntry.SetText(text)
		a.onTextChanged()
		a.status.SetText("pasted")
	} else {
		a.status.SetText("Clipboard is empty")
	}
}

func (a *App) clear() {
	a.textEntry.SetText("")
	a.onTextChanged()
	if a.window.Clipboard() != nil {
		a.window.Clipboard().SetContent("")
	}
	a.status.SetText("Cleared")
}

func (a *App) setupUI() {
	pasteBtn := widget.NewButton("Paste", a.pasteFromClipboard)
	saveBtn := widget.NewButton("Save QR", a.saveQR)
	clearBtn := widget.NewButton("Clear", a.clear)
	pasteBtn.Importance = widget.HighImportance
	saveBtn.Importance = widget.HighImportance
	clearBtn.Importance = widget.HighImportance

	a.status = widget.NewLabel("Ready")
	a.textEntry = widget.NewMultiLineEntry()
	a.textEntry.SetPlaceHolder("Paste text here...")
	a.textEntry.Wrapping = fyne.TextWrapWord
	a.textEntry.OnChanged = func(string) { a.onTextChanged() }
	
	a.qrImage = canvas.NewImageFromImage(nil)
	a.qrImage.SetMinSize(fyne.NewSize(512, 512))
	a.qrImage.FillMode = canvas.ImageFillOriginal

	topBar := container.NewHBox(layout.NewSpacer(), a.infoBtn, layout.NewSpacer(), a.themeBtn)
	buttonRow := container.NewHBox(layout.NewSpacer(), pasteBtn, saveBtn, clearBtn, layout.NewSpacer())

	content := container.NewBorder(
		container.NewVBox(topBar, widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), container.NewHBox(a.status, layout.NewSpacer())),
		nil, nil,
		container.NewVBox(
			widget.NewLabelWithStyle("Google Lens to AEC", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			a.textEntry,
			buttonRow,
			widget.NewSeparator(),
			a.qrImage,
		),
	)
	a.window.SetContent(content)
	a.onTextChanged()
}

func main() {
	myApp := app.New()
	window := myApp.NewWindow("GL2AEC")
	window.Resize(fyne.NewSize(600, 850))
	window.CenterOnScreen()
	
	appInstance := &App{app: myApp, window: window, dark: true}
	appInstance.themeBtn = widget.NewButton("☀️", appInstance.toggleTheme)
	appInstance.themeBtn.Importance = widget.LowImportance
	appInstance.infoBtn = widget.NewButtonWithIcon("", theme.InfoIcon(), appInstance.showInfo)
	appInstance.infoBtn.Importance = widget.LowImportance

	myApp.Settings().SetTheme(&purpleTheme{base: theme.DarkTheme()})

	appInstance.setupUI()
	
	myApp.Lifecycle().SetOnEnteredForeground(func() {
		appInstance.onTextChanged()
	})

	window.ShowAndRun()
}
