package query

import analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"

// BuildDevices emits three queries: device_category, browser, and os.
// The UI wants all three on one page, so handlers run them
// concurrently and pack into a single response.
//
// device_category is on the hourly/daily MVs; browser + os are not, so
// those two always hit raw events.
func (b *Builder) BuildDevices(f *analyticsspec.Filters, serverID string) (deviceCategory, browser, os *Built, err error) {
	deviceCategory, err = b.BuildTable(DimDeviceCategory, f, serverID, 50, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	browser, err = b.BuildTable(DimBrowser, f, serverID, 50, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	os, err = b.BuildTable(DimOS, f, serverID, 50, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	return deviceCategory, browser, os, nil
}
