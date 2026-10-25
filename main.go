package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	cursor   int
	choices  []string
	selected map[int]struct{}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		{
			switch message.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(m.choices)-1 {
					m.cursor++
				}
			case "enter", "space":
				_, ok := m.selected[m.cursor]
				if ok {
					delete(m.selected, m.cursor)
				} else {
					m.selected[m.cursor] = struct{}{}
				}
			}
		}
	}
	return m, nil
}

func (m model) View() tea.View {
	builder := strings.Builder{}
	builder.WriteString("What should we buy at the market?\n\n")

	for i, choice := range m.choices {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		checked := " "
		if _, ok := m.selected[i]; ok {
			checked = "x"
		}

		fmt.Fprintf(&builder, "%s [%s] %s\n", cursor, checked, choice)
	}

	builder.WriteString("\nPress q to quit.\n")
	view := tea.NewView(builder.String())
	view.WindowTitle = "Grocery List"
	return view
}

func main() {
	program := tea.NewProgram(model{
		choices: []string{
			"Tuscan Kale",
			"Yukon Gold Potatoes",
			"Quinoa",
			"Red Lentils",
		},
		selected: make(map[int]struct{}),
	})
	if _, err := program.Run(); err != nil {
		fmt.Printf("Alas, there has been an error: %v", err)
	}
}
