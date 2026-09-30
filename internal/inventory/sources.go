package inventory

import (
	"errors"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// CheckSources permits incomplete metadata from individual legacy projects.
// Source failures still reject an offline import; observations and reservations
// remain recorded by Sync so a retry cannot discard previously discovered data.
func CheckSources(result model.Inventory) error {
	for _, warning := range result.Warnings {
		if warning != "PROJECT_METADATA_INCOMPLETE" {
			return errors.New("inventory sync has source failures; last successful metadata retained")
		}
	}
	return nil
}
