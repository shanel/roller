package roller

import (
	"encoding/json"
	"testing"

	"google.golang.org/appengine/aetest"
	"google.golang.org/appengine/datastore"
)

func TestGetEncodedRoomKeyFromName(t *testing.T) {
	ctx, done, err := aetest.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	got, err := getEncodedRoomKeyFromName(ctx, "HappyFunBall")
	if err == nil {
		t.Fatalf("getEncodedRoomFromName(ctx, 'HappyFunBall') == %v, %v; want _, err", got, err)
	}
	key := datastore.NewKey(ctx, "Room", "", 1, nil)
	if _, err := datastore.Put(ctx, key, &Room{Slug: "HappyFunBall"}); err != nil {
		t.Fatal(err)
	}
	var r Room
	if err = datastore.Get(ctx, key, &r); err != nil {
		t.Fatal(err)
	}
	got, err = getEncodedRoomKeyFromName(ctx, "HappyFunBall")
	if err != nil || got != key.Encode() {
		t.Fatalf("getEncodedRoomFromName(ctx, 'HappyFunBall') == %v, %v; want %v, nil", got, err, key.Encode())
	}
}

func TestNoSpaces(t *testing.T) {
	got := noSpaces("Foo Bar Bat")
	want := "FooBarBat"
	if got != want {
		t.Fatalf("noSpaces('Foo Bar Bat') == %v; want %v", got, want)
	}
}

func TestUpdateRoom(t *testing.T) {
	ctx, done, err := aetest.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	up, err := json.Marshal([]Update{})
	if err != nil {
		t.Fatalf("could not marshal update: %v", err)
	}
	key := datastore.NewKey(ctx, "Room", "", 1, nil)
	if _, err := datastore.Put(ctx, key, &Room{Slug: "HappyFunBall", Updates: up}); err != nil {
		t.Fatal(err)
	}
	err = updateRoom(ctx, key.Encode(), Update{})
	if err != nil {
		t.Fatalf("updateRoom(ctx, %v, Update{}) == %v; want nil", key.Encode(), err)
	}
	var r Room
	if err = datastore.Get(ctx, key, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Updates) == 0 {
		t.Fatal("room did not have any updates, should have one")
	}
}

func TestNewRoom(t *testing.T) {
	ctx, done, err := aetest.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	rn, err := newRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = getEncodedRoomKeyFromName(ctx, rn)
	if err != nil {
		t.Fatalf("getEncodedRoomFromName(ctx, %s) == _, %v; want _, nil", rn, err)
	}
}
