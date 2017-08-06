package roller

import (
	"testing"
	
	//"golang.org/x/net/context"
	//"google.golang.org/appengine"
	"google.golang.org/appengine/aetest"
	"google.golang.org/appengine/datastore"
)

func TestGetEncodedRoomKeyFromName(t *testing.T) {
	ctx, done, err := aetest.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	key := datastore.NewKey(ctx, "Room", "", 1, nil)
	if _, err := datastore.Put(ctx, key, &Room{Slug: "HappyFunBall"}); err != nil {
		t.Fatal(err)
	}
	got, err := getEncodedRoomKeyFromName(ctx, "HappyFunBall")
	if err != nil || got != key.Encode() {
		t.Fatalf("getEncodedRoomFromName(ctx, 'HappyFunBall') == %v, %v; want %v, nil", got, err, key.Encode())
	}
}
