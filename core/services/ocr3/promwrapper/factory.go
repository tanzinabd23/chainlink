package promwrapper

import (
	"context"

	"github.com/smartcontractkit/libocr/offchainreporting2plus/ocr3types"
)

var _ ocr3types.ReportingPluginFactory[any] = &ReportingPluginFactory[any]{}

type ReportingPluginFactory[RI any] struct {
	origin  ocr3types.ReportingPluginFactory[RI]
	chainID string
	plugin  string
}

func NewReportingPluginFactory[RI any](
	origin ocr3types.ReportingPluginFactory[RI],
	chainID string,
	plugin string,
) *ReportingPluginFactory[RI] {
	return &ReportingPluginFactory[RI]{
		origin:  origin,
		chainID: chainID,
		plugin:  plugin,
	}
}

func (r ReportingPluginFactory[RI]) NewReportingPlugin(ctx context.Context, config ocr3types.ReportingPluginConfig) (ocr3types.ReportingPlugin[RI], ocr3types.ReportingPluginInfo, error) {
	plugin, info, err := r.origin.NewReportingPlugin(ctx, config)
	if err != nil {
		return nil, ocr3types.ReportingPluginInfo{}, err
	}
	wrapped := newReportingPlugin(
		plugin,
		r.chainID,
		r.plugin,
		promOCR3ReportsGenerated,
		promOCR3Durations,
	)
	return wrapped, info, err
}
