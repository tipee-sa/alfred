package scheduler

import "github.com/gammadia/alfred/namegen"

type Job struct {
	Name   namegen.ID
	Image  string
	Tasks  []string
	Script string
}
