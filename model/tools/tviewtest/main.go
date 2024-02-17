// Demo code for the Form primitive.
// Demo code for the Form primitive.
package main

import (
	"fmt"
	"os"

	"github.com/rivo/tview"
)

func main() {
	app := tview.NewApplication()
	form := tview.NewForm().
		//		AddDropDown("Title", []string{"Mr.", "Ms.", "Mrs.", "Dr.", "Prof."}, 0, nil).
		AddInputField("Form name", "Original", 50, nil, nil).
		//		AddInputField("Last name", "", 20, nil, nil).
		AddTextArea("JSON schema", "", 0, 20, 0, nil).
		AddInputField("Description (optional)", "", 100, nil, nil).
		// AddTextView("Notes", "This is just a demo.\nYou can enter whatever you wish.", 40, 2, true, false).
		// AddCheckbox("Age 18+", false, nil).
		// AddPasswordField("Password", "", 10, '*', nil).
		AddButton("Cancel", func() {
			os.Exit(1)
		}).
		AddButton("Done (or CTRL-C)", func() {
			app.Stop()
		})

	form.SetTitle("Enter some data").SetTitleAlign(tview.AlignLeft) //SetBorder(true)
	if err := app.SetRoot(form, true).Run(); err != nil {           // .EnableMouse(true)
		fmt.Printf("Error from terminal (tview): %s", err)
		os.Exit(1)
	}

	name := form.GetFormItem(0).(*tview.InputField).GetText()
	txt := form.GetFormItem(1).(*tview.TextArea).GetText()
	desc := form.GetFormItem(2).(*tview.InputField).GetText()
	fmt.Printf("txt: %s\n", txt)
	fmt.Printf("name: %s\n", name)
	fmt.Printf("desc: %s\n", desc)
}
