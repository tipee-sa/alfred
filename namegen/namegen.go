package namegen

import (
	vendor "github.com/anandvarma/namegen"
)

var gen = vendor.New()

type ID string

func Get() ID {
	return ID(gen.Get())
}

func (id ID) String() string {
	return string(id)
}
