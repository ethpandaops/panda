package cli

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/ethpandaops/panda/pkg/config"
)

// configDisplay is the main TUI state.
type configDisplay struct {
	app        *tview.Application
	pages      *tview.Pages
	categories []configCategory
	configPath string // resolved base config path (for validation)
	userPath   string
	existing   map[string]any
	saved      bool
}

// newConfigDisplay creates the TUI display and wires up all pages.
func newConfigDisplay(categories []configCategory, configPath, userPath string, existing map[string]any) *configDisplay {
	snapshotOriginals(categories)

	d := &configDisplay{
		app:        tview.NewApplication(),
		pages:      tview.NewPages(),
		categories: categories,
		configPath: configPath,
		userPath:   userPath,
		existing:   existing,
	}

	d.buildHomePage()

	for i := range categories {
		d.buildCategoryPage(i)
	}

	d.buildReviewPage()

	return d
}

// run starts the tview application.
func (d *configDisplay) run() {
	// Use the terminal's native colors instead of forcing black/white.
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.ContrastBackgroundColor = tcell.ColorDefault
	tview.Styles.MoreContrastBackgroundColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorDefault
	tview.Styles.BorderColor = tcell.ColorDefault
	tview.Styles.TitleColor = tcell.ColorDefault
	tview.Styles.GraphicsColor = tcell.ColorDefault
	tview.Styles.SecondaryTextColor = tcell.ColorYellow
	tview.Styles.TertiaryTextColor = tcell.ColorGreen
	tview.Styles.InverseTextColor = tcell.ColorDefault
	tview.Styles.ContrastSecondaryTextColor = tcell.ColorDefault

	d.pages.SwitchToPage("home")

	if err := d.app.SetRoot(d.pages, true).EnableMouse(false).Run(); err != nil {
		fmt.Printf("TUI error: %v\n", err)
	}
}

// buildHomePage creates the category browser.
func (d *configDisplay) buildHomePage() {
	list := tview.NewList().
		ShowSecondaryText(false).
		SetHighlightFullLine(true)

	for _, cat := range d.categories {
		list.AddItem("  "+cat.Name, "", 0, nil)
	}

	list.AddItem("", "", 0, nil)
	list.AddItem("  Review Changes and Save", "", 0, nil)

	list.SetBorder(true).SetTitle(" Categories ")

	desc := tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true)

	desc.SetBorder(true).
		SetTitle(" Description ").
		SetBorderPadding(1, 1, 2, 2)

	if len(d.categories) > 0 {
		desc.SetText(d.categories[0].Description)
	}

	list.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		switch {
		case index < len(d.categories):
			desc.SetText(d.categories[index].Description)
		case strings.TrimSpace(mainText) != "":
			desc.SetText("Review all pending changes and save to config.user.yaml.\n\nYour overrides survive 'panda init' and 'panda upgrade'.")
		default:
			desc.SetText("")
		}
	})

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < len(d.categories) {
			d.pages.SwitchToPage(fmt.Sprintf("category-%d", index))
		} else if strings.TrimSpace(mainText) != "" {
			d.refreshReviewPage()
			d.pages.SwitchToPage("review")
		}
	})

	content := tview.NewFlex().
		AddItem(list, 0, 1, true).
		AddItem(desc, 0, 2, false)

	frame := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(titleBar("Categories"), 1, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(hintBar("Up/Down: Navigate   Enter: Select   Ctrl+C: Quit"), 1, 0, false)

	d.pages.AddPage("home", frame, true, true)
}

// buildCategoryPage creates the settings editor for a single category.
func (d *configDisplay) buildCategoryPage(catIdx int) {
	cat := &d.categories[catIdx]
	pageID := fmt.Sprintf("category-%d", catIdx)

	list := tview.NewList().
		ShowSecondaryText(false).
		SetHighlightFullLine(true)

	for _, p := range cat.Params {
		list.AddItem(formatSettingLine(p), "", 0, nil)
	}

	list.SetBorder(true).SetTitle(fmt.Sprintf(" %s ", cat.Name))

	desc := tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true)

	desc.SetBorder(true).
		SetTitle(" Details ").
		SetBorderPadding(1, 1, 2, 2)

	if len(cat.Params) > 0 {
		desc.SetText(descriptionText(cat.Params[0]))
	}

	list.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < len(cat.Params) {
			desc.SetText(descriptionText(cat.Params[index]))
		}
	})

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= len(cat.Params) {
			return
		}

		p := cat.Params[index]

		if p.Type == paramBool {
			if p.Value == "true" {
				p.Value = "false"
			} else {
				p.Value = "true"
			}

			list.SetItemText(index, formatSettingLine(p), "")
		} else {
			d.showEditModal(p, func() {
				list.SetItemText(index, formatSettingLine(p), "")
			})
		}
	})

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			d.pages.SwitchToPage("home")
			return nil
		}

		return event
	})

	content := tview.NewFlex().
		AddItem(list, 0, 1, true).
		AddItem(desc, 0, 1, false)

	hint := "Up/Down: Navigate   Enter: Edit   Esc: Back"
	if hasBoolParams(cat) {
		hint = "Up/Down: Navigate   Enter: Edit/Toggle   Esc: Back"
	}

	frame := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(titleBar("Categories > "+cat.Name), 1, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(hintBar(hint), 1, 0, false)

	d.pages.AddPage(pageID, frame, true, false)
}

// showEditModal displays a modal form for editing a parameter.
func (d *configDisplay) showEditModal(p *configParam, onSave func()) {
	closeModal := func() {
		d.pages.RemovePage("edit-modal")
		d.pages.RemovePage("edit-bg")
	}

	accept := func(textToCheck string, lastChar rune) bool {
		switch p.Type {
		case paramInt, paramPort:
			return lastChar >= '0' && lastChar <= '9'
		case paramFloat:
			return (lastChar >= '0' && lastChar <= '9') || lastChar == '.'
		default:
			return true
		}
	}

	form := tview.NewForm()
	form.AddInputField("Value", p.Value, 20, accept, nil)
	form.AddButton("Save", func() {
		item, ok := form.GetFormItemByLabel("Value").(*tview.InputField)
		if !ok {
			return
		}

		value := item.GetText()

		if msg := validateParamValue(p.Type, value); msg != "" {
			d.app.SetFocus(item)

			return
		}

		p.Value = value
		onSave()
		closeModal()
	})
	form.AddButton("Cancel", closeModal)
	form.SetCancelFunc(closeModal)

	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s ", p.Name)).
		SetBorderPadding(0, 0, 1, 1)

	// Opaque background so the page behind doesn't bleed through.
	bg := tview.NewBox()

	modal := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(form, 5, 0, true).
			AddItem(nil, 0, 1, false),
			40, 0, true).
		AddItem(nil, 0, 1, false)

	// Stack: opaque background behind the modal.
	d.pages.AddPage("edit-bg", bg, true, true)
	d.pages.AddPage("edit-modal", modal, true, true)
	d.app.SetFocus(form)
}

// buildReviewPage creates the review + save page.
func (d *configDisplay) buildReviewPage() {
	d.pages.AddPage("review", d.createReviewContent(), true, false)
}

// refreshReviewPage rebuilds the review page with current changes.
func (d *configDisplay) refreshReviewPage() {
	d.pages.RemovePage("review")
	d.pages.AddPage("review", d.createReviewContent(), true, false)
}

func (d *configDisplay) createReviewContent() tview.Primitive {
	changed := changedParams(d.categories)

	body := tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true)

	body.SetBorder(true).
		SetTitle(" Changes ").
		SetBorderPadding(1, 1, 2, 2)

	if len(changed) == 0 {
		body.SetText("[yellow]No changes to save.[-]")
	} else {
		var sb strings.Builder

		for _, cat := range d.categories {
			var catChanges []string

			for _, p := range cat.Params {
				if p.Value != p.Original {
					catChanges = append(catChanges, fmt.Sprintf(
						"  %-28s %s -> [green]%s[-]",
						p.Name, p.Original, p.Value,
					))
				}
			}

			if len(catChanges) > 0 {
				fmt.Fprintf(&sb, "[yellow]%s[-]\n", cat.Name)

				for _, c := range catChanges {
					sb.WriteString(c + "\n")
				}

				sb.WriteString("\n")
			}
		}

		fmt.Fprintf(&sb, "%d setting(s) changed.", len(changed))
		body.SetText(sb.String())
	}

	var focusTarget tview.Primitive

	buttons := tview.NewFlex()

	if len(changed) > 0 {
		saveBtn := tview.NewButton(" Save ")
		cancelBtn := tview.NewButton(" Back ")

		saveBtn.SetSelectedFunc(func() {
			overrides, err := buildOverrideMap(d.categories, d.existing)
			if err != nil {
				body.SetText(fmt.Sprintf("[red]Error: %v[-]", err))
				return
			}

			if err := config.ValidateMergedConfig(d.configPath, overrides); err != nil {
				body.SetText(fmt.Sprintf("[red]Validation failed: %v[-]\n\nGo back and fix the values.", err))
				return
			}

			if err := config.SaveUserConfig(d.userPath, overrides); err != nil {
				body.SetText(fmt.Sprintf("[red]Error saving: %v[-]", err))
				return
			}

			d.saved = true
			d.app.Stop()
		})

		cancelBtn.SetSelectedFunc(func() {
			d.pages.SwitchToPage("home")
		})

		buttons.
			AddItem(nil, 0, 1, false).
			AddItem(saveBtn, 8, 0, true).
			AddItem(nil, 2, 0, false).
			AddItem(cancelBtn, 8, 0, false).
			AddItem(nil, 0, 1, false)

		wireButtonNav(d, saveBtn, cancelBtn)
		focusTarget = saveBtn
	} else {
		backBtn := tview.NewButton(" Back ")

		backBtn.SetSelectedFunc(func() {
			d.pages.SwitchToPage("home")
		})

		backBtn.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				d.pages.SwitchToPage("home")

				return nil
			}

			return event
		})

		buttons.
			AddItem(nil, 0, 1, false).
			AddItem(backBtn, 8, 0, true).
			AddItem(nil, 0, 1, false)

		focusTarget = backBtn
	}

	frame := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(titleBar("Review Changes"), 1, 0, false).
		AddItem(body, 0, 1, false).
		AddItem(buttons, 1, 0, true).
		AddItem(hintBar("Tab: Switch   Enter: Select   Esc: Back"), 1, 0, false)

	frame.SetFocusFunc(func() {
		d.app.SetFocus(focusTarget)
	})

	return frame
}

// titleBar creates a breadcrumb title line.
func titleBar(breadcrumb string) *tview.TextView {
	return tview.NewTextView().
		SetDynamicColors(true).
		SetText(fmt.Sprintf(" [::b]panda config[::-]  >  %s", breadcrumb))
}

// hintBar creates the bottom hint line.
func hintBar(text string) *tview.TextView {
	return tview.NewTextView().
		SetTextAlign(tview.AlignCenter).
		SetText(text).
		SetTextColor(tview.Styles.TertiaryTextColor)
}

// wireButtonNav sets up Tab/Esc navigation between two buttons.
func wireButtonNav(d *configDisplay, left, right *tview.Button) {
	left.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyRight:
			d.app.SetFocus(right)

			return nil
		case tcell.KeyEscape:
			d.pages.SwitchToPage("home")

			return nil
		}

		return event
	})

	right.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyLeft, tcell.KeyBacktab:
			d.app.SetFocus(left)

			return nil
		case tcell.KeyEscape:
			d.pages.SwitchToPage("home")

			return nil
		}

		return event
	})
}

// formatSettingLine formats a param as "  Name .............. value".
func formatSettingLine(p *configParam) string {
	const totalWidth = 50

	name := p.Name
	value := formatParamValue(p)

	dots := totalWidth - len(name) - tview.TaggedStringWidth(value)
	if dots < 3 {
		dots = 3
	}

	return fmt.Sprintf("  %s %s %s", name, strings.Repeat(".", dots), value)
}

// formatParamValue formats just the value with color tags.
func formatParamValue(p *configParam) string {
	if p.Type == paramBool {
		if p.Value == "true" {
			return "[green]on[-]"
		}

		return "[red]off[-]"
	}

	display := p.Value
	if display == "" && p.Type == paramOptionalString {
		display = "(default)"
	}

	if p.Value != p.Original {
		return "[green]" + display + "[-]"
	}

	return display
}

// descriptionText builds the description panel text for a param.
func descriptionText(p *configParam) string {
	text := p.Description + "\n\nCurrent: " + p.Value

	if p.Value != p.Original {
		text += "\nOriginal: " + p.Original
	}

	return text
}

// hasBoolParams returns true if the category has any bool parameters.
func hasBoolParams(cat *configCategory) bool {
	for _, p := range cat.Params {
		if p.Type == paramBool {
			return true
		}
	}

	return false
}
