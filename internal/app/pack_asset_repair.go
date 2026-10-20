package app

import (
	"errors"
	"fmt"
)

// Rebuild references for already-disabled files without restoring or moving any
// images. Existing patches identify aliases removed by earlier MCUI versions.
func planAssetReferenceRepair(layerDir, layer string, state assetState) ([]assetFileEdit, assetState, []string, []string, error) {
	records := []assetRecord{}
	selected := map[string]bool{}
	for _, record := range state.Assets {
		if record.Layer == layer {
			record.Patches = append([]assetPatch(nil), record.Patches...)
			records = append(records, record)
			selected[record.Path] = true
		}
	}
	if len(records) == 0 {
		return nil, state, nil, nil, nil
	}
	prior := []assetRecord{}
	for _, patch := range state.CatalogPatches {
		belongs := patch.Layer == layer
		if patch.Layer == "" && len(patch.Requires) > 0 {
			belongs = true
			for _, path := range patch.Requires {
				if !selected[path] {
					belongs = false
				}
			}
		}
		if belongs {
			prior = append(prior, assetRecord{Layer: layer, Patches: []assetPatch{patch}})
		}
	}
	edits, records, patches, warnings, err := referenceEdits(layerDir, "archive", records, prior...)
	if err != nil {
		return nil, state, nil, nil, err
	}
	next := assetState{UUID: state.UUID, Assets: append([]assetRecord(nil), state.Assets...), CatalogPatches: append([]assetPatch(nil), state.CatalogPatches...)}
	updated := map[string]assetRecord{}
	for _, record := range records {
		updated[record.Path] = record
	}
	for i, record := range next.Assets {
		if record.Layer == layer {
			next.Assets[i] = updated[record.Path]
		}
	}
	next.CatalogPatches = append(next.CatalogPatches, patches...)
	changes := []string{}
	for _, patch := range patches {
		changes = append(changes, patch.File+" · "+patch.Key)
	}
	if len(changes) == 0 {
		for _, edit := range edits {
			changes = append(changes, edit.Path)
		}
	}
	return edits, next, changes, warnings, nil
}

func applyAssetReferenceRepair(statePath string, edits []assetFileEdit, state assetState) error {
	written := []assetFileEdit{}
	rollback := func(cause error) error {
		for i := len(written) - 1; i >= 0; i-- {
			if err := writeAssetJSON(written[i].Path, written[i].Before); err != nil {
				cause = errors.Join(cause, fmt.Errorf("could not restore %s: %w", written[i].Path, err))
			}
		}
		return cause
	}
	for _, edit := range edits {
		if err := writeAssetJSON(edit.Path, edit.After); err != nil {
			return rollback(err)
		}
		written = append(written, edit)
	}
	if err := writeAssetState(statePath, state); err != nil {
		return rollback(err)
	}
	return nil
}
