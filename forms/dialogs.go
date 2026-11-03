package forms

import (
	"github.com/ipoluianov/nui/ui"
)

// choice is a button of a question: its text, its role (accept is Enter,
// reject is Esc) and what it does after the box closes
type choice struct {
	text string
	role ui.ButtonRole
	do   func()
}

// ask shows a question with the buttons of the choices; the box closes
// before the choice runs. A box without a reject choice is closed by Esc
// without doing anything.
func ask(parent *ui.Form, title, text string, choices ...choice) {
	askWith(parent, title, text, nil, choices...)
}

// askWith is ask with a widget under the text: a check box of an option
func askWith(parent *ui.Form, title, text string, extra ui.Widgeter, choices ...choice) {
	c := &ui.DialogContent{}
	c.InitWidget()
	label := ui.NewLabel(text)
	label.SetMinWidth(360)
	c.AddWidget(0, 0, label)
	if extra != nil {
		c.AddWidget(1, 0, extra)
	} else {
		c.AddVSpacer(1, 0)
	}
	buttons := ui.NewDialogButtonBox()
	for _, ch := range choices {
		ch := ch
		buttons.AddButton(ch.text, ch.role, func() {
			c.RunInParent(ch.do)
			c.Form().Close()
		})
	}
	c.AddWidget(2, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(title) }
	parent.Panel().ShowDialog(c)
}
