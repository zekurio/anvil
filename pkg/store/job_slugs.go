package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
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

func backfillJobSlugs(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM jobs WHERE slug = '' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list jobs without slugs: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan job without slug: %w", errors.Join(err, rows.Close()))
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close jobs without slugs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate jobs without slugs: %w", err)
	}

	used := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		assigned := false
		for range 100 {
			slug, err := newJobSlug()
			if err != nil {
				return err
			}
			if _, exists := used[slug]; exists {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET slug = ? WHERE id = ?`, slug, id); err != nil {
				return fmt.Errorf("backfill job %d slug: %w", id, err)
			}
			used[slug] = struct{}{}
			assigned = true
			break
		}
		if !assigned {
			return fmt.Errorf("could not allocate unique slug for job %d", id)
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX jobs_slug_idx ON jobs(slug)`); err != nil {
		return fmt.Errorf("create job slug index: %w", err)
	}
	return nil
}
