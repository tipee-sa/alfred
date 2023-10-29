package main

import (
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
)

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Show the status of the server",

	RunE: func(cmd *cobra.Command, args []string) error {
		app := tview.NewApplication()
		layout := tview.NewFlex().SetDirection(tview.FlexRow)

		topbar := tview.NewFlex()
		layout.AddItem(topbar, 1, 0, false)

		title := tview.NewTextView().SetText("Alfred")
		topbar.AddItem(title, 0, 100, false)

		return app.SetRoot(layout, true).Run()
	},
}
