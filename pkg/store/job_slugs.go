package store

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

var slugAdjectives = []string{
	"brave", "bright", "calm", "clever", "cosy", "crisp", "dapper", "eager",
	"fair", "gentle", "happy", "jolly", "kind", "lively", "lucky", "merry",
	"mighty", "nimble", "proud", "quick", "quiet", "shiny", "swift", "wise",
}

var slugColors = []string{
	"amber", "aqua", "azure", "blue", "coral", "cyan", "gold", "green",
	"indigo", "ivory", "jade", "lilac", "lime", "mint", "navy", "ochre",
	"olive", "orange", "peach", "pink", "plum", "red", "silver", "violet",
}

var slugAnimals = []string{
	"badger", "bear", "bison", "cat", "cobra", "dolphin", "eagle", "falcon",
	"fox", "gecko", "heron", "koala", "lynx", "otter", "owl", "panda",
	"panther", "puma", "raven", "seal", "shark", "tiger", "wolf", "yak",
}

func newJobSlug() (string, error) {
	adjective, err := randomWord(slugAdjectives)
	if err != nil {
		return "", err
	}
	color, err := randomWord(slugColors)
	if err != nil {
		return "", err
	}
	animal, err := randomWord(slugAnimals)
	if err != nil {
		return "", err
	}
	return adjective + "-" + color + "-" + animal, nil
}

func randomWord(words []string) (string, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	if err != nil {
		return "", fmt.Errorf("generate job slug: %w", err)
	}
	return words[index.Int64()], nil
}
