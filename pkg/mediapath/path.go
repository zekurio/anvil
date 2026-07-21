package mediapath

import (
	"path/filepath"

	"github.com/zekurio/anvil/pkg/domain"
)

func Relative(source domain.MediaSource, asset domain.MediaAsset) string {
	if source.Kind == domain.SourceKindPackage && asset.RelativePath != "" {
		return filepath.Join(filepath.FromSlash(source.RelativePath), filepath.FromSlash(asset.RelativePath))
	}
	return filepath.FromSlash(source.RelativePath)
}

func Input(root string, source domain.MediaSource, asset domain.MediaAsset) string {
	return filepath.Join(root, Relative(source, asset))
}
