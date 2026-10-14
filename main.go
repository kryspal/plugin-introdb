// Command introdb is the official community intro data plugin (change request 0122 part A, capabilities segments.provider, segments.submit):
// given the IMDb ID of a series or movie plus season and episode, it asks IntroDB (introdb.app) for the times of the intro, outro and recap;
// once the administrator turns it on and enters their own key, it can submit manually marked segments. Each user's own server queries
// directly, caching only locally (in memory) and throttling itself, with one request at a time.
package main

import "github.com/kryspal/plugin-sdk/sdk"

func main() {
	p := newPlugin()
	sdk.Main(sdk.Config{Plugin: p, SegmentProvider: p})
}
