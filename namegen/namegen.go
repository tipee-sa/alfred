package namegen

import (
	vendor "github.com/anandvarma/namegen"
)

var gen = vendor.New()

func Get() string {
	return gen.Get()
}
