//    AppEngine based Dice Roller
//    Copyright (C) 2017  Shane Liebling
//
//    This program is free software: you can redistribute it and/or modify
//    it under the terms of the GNU General Public License as published by
//    the Free Software Foundation, either version 3 of the License, or
//    (at your option) any later version.
//
//    This program is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//    GNU General Public License for more details.
//
//    You should have received a copy of the GNU General Public License
//    along with this program.  If not, see <http://www.gnu.org/licenses/>.

// TODO(shanel): Need to clean up the order fo this file, move the js into its own file, nuke useless comments, write tests...
// Maybe keep track of connected users of a room to determine smallest window size and restrict dice movement to that size?
// Probably would be good to factor out duplicate code.
// Should also make a little "about" page noting where everything comes from.
package roller

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dustinkirkland/golang-petname"
	"github.com/golang/freetype"
	"golang.org/x/image/font"

	"appengine"
	"appengine/datastore"
	"appengine/file"
	// Maybe use this later?
	//"appengine/user"
)

// As we create urls for the die images, store them here so we don't keep making them
var diceURLs = map[string]string{}
var refreshDelta = int64(2)
var refresher = refreshCounter{}

type Update struct {
	Timestamp int64
	Updater   string
}

type refreshCounter struct {
	sync.Mutex
	c int64
}

func (r *refreshCounter) increment() int64 {
	r.Lock()
	defer r.Unlock()
	r.c++
	return r.c
}

type Room struct {
	Updates   []byte // hooray having to use json
	Timestamp int64
	Slug      string
}

type Die struct {
	Size      string // for fate dice this won't be an integer
	Result    int    // For fate dice make this one of three very large numbers?
	ResultStr string
	X         float64
	Y         float64
	Key       *datastore.Key
	KeyStr    string
	Timestamp int64
	Image     string
	New       bool
}

func (d *Die) updatePosition(x, y float64) {
	d.X = x
	d.Y = y
	d.New = false
}

func (d *Die) getPosition() (float64, float64) {
	return d.X, d.Y
}

type Passer struct {
	Dice []Die
}

func noSpaces(str string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, str)
}

func generateRoomName() string {
	// 3 part words will allow for 91922432 unique names
	// 4 part words will allow for 41181249536 unique names
	name := petname.Generate(3, " ")
	name = strings.Title(name)
	return noSpaces(name)
}

func getEncodedRoomKeyFromName(c appengine.Context, name string) (string, error) {
	q := datastore.NewQuery("Room").Filter("Slug =", name).Limit(1).KeysOnly()
	k, err := q.GetAll(c, nil)
	if err != nil {
		return name, fmt.Errorf("problem executing room (by Slug) query: %v", err)
	}
	if len(k) > 0 {
		return k[0].Encode(), nil
	}
	return name, fmt.Errorf("couldn't find a room key for %v", name)
}

func updateRoom(c appengine.Context, rk string, u Update) error {
	roomKey, err := datastore.DecodeKey(rk)
	if err != nil {
		return fmt.Errorf("updateRoom: could not decode room key %v: %v", rk, err)
	}
	var r Room
	t := time.Now().Unix()
	if err = datastore.Get(c, roomKey, &r); err != nil {
		// Couldn't find it, so create it
		c.Errorf("couldn't find room %v, so going to create it", rk)
		up, err := json.Marshal([]Update{})
		if err != nil {
			return fmt.Errorf("could not marshal update: %v", err)
		}
		r = Room{Updates: up, Timestamp: t, Slug: generateRoomName()}
		_, err = datastore.Put(c, roomKey, &r)
		if err != nil {
			return fmt.Errorf("could not create updated room %v: %v", rk, err)
		}
	}
	var umUpdates []Update
	err = json.Unmarshal(r.Updates, &umUpdates)
	if err != nil {
		return fmt.Errorf("could not unmarshal updates in updateRoom: %v", err)
	}
	umUpdates = append(umUpdates, u)
	r.Updates, err = json.Marshal(umUpdates)
	if err != nil {
		return fmt.Errorf("could not marshal updates in updateRoom: %v", err)
	}
	r.Timestamp = t
	_, err = datastore.Put(c, roomKey, &r)
	if err != nil {
		return fmt.Errorf("could not update room %v: %v", rk, err)
	}
	return nil
}

func refreshRoom(c appengine.Context, rk, fp string) string {
	roomKey, err := datastore.DecodeKey(rk)
	out := ""
	if err != nil {
		c.Errorf("refreshRoom: could not decode room key %v: %v", rk, err)
		return out
	}
	var r Room
	if err = datastore.Get(c, roomKey, &r); err != nil {
		c.Errorf("could not find room %v for refresh: %v", rk, err)
		return out
	}
	keep := []Update{}
	now := time.Now().Unix()
	var umUpdates []Update
	var send []Update
	err = json.Unmarshal(r.Updates, &umUpdates)
	if err != nil {
		c.Errorf("could not unmarshal updates in refreshRoom: %v", err)
		return ""
	}
	for _, u := range umUpdates {
		q := (now - u.Timestamp)
		if q > refreshDelta {
			continue
		}
		keep = append(keep, u)
		if u.Updater != fp {
			send = append(send, u)
		}
	}
	r.Updates, err = json.Marshal(keep)
	if err != nil {
		c.Errorf("could not marshal updates in refreshRoom: %v", err)
		return ""
	}
	_, err = datastore.Put(c, roomKey, &r)
	if err != nil {
		c.Errorf("could not create updated room %v: %v", rk, err)
	}
	if len(send) > 0 {
		toHash, err := json.Marshal(send)
		if err != nil {
			c.Errorf("could not marshal updates to send in refreshRoom: %v", err)
			return ""
		}
		out = fmt.Sprintf("%x", md5.Sum(toHash))
	}
	return out
}

// roomKey creates a new room entity key.
func roomKey(c appengine.Context) *datastore.Key {
	return datastore.NewKey(c, "Room", "", time.Now().UnixNano(), nil)
}

// dieKey creates a new die entity key.
func dieKey(c appengine.Context, roomKey *datastore.Key, i int64) *datastore.Key {
	return datastore.NewKey(c, "Die", "", time.Now().UnixNano()+i, roomKey)
}

// TODO(shanel): Have a button to create a new room
func newRoom(c appengine.Context) (string, error) {
	up, err := json.Marshal([]Update{})
	if err != nil {
		return "", fmt.Errorf("could not marshal update: %v", err)
	}
	roomName := generateRoomName()
	var k *datastore.Key
	k, err = datastore.Put(c, roomKey(c), &Room{Updates: up, Timestamp: time.Now().Unix(), Slug: roomName})
	if err != nil {
		return "", fmt.Errorf("could not create new room: %v", err)
	}
	var testRoom Room
	if err = datastore.Get(c, k, &testRoom); err != nil {
		return "", fmt.Errorf("couldn't find the new entry: %v", err)
	}
	// TODO(shanel): why does it seem I need the above three lines? Race condition?

	return roomName, nil
}

func newRoll(c appengine.Context, sizes map[string]string, roomKey *datastore.Key, color string) error {
	dice := []*Die{}
	keys := []*datastore.Key{}
	var totalCount int
	for size, v := range sizes {
		var oldSize string
		if size != "label" {
			if size == "6p" {
				oldSize = "6p"
				size = "6"
			}
			count, err := strconv.Atoi(v)
			if err != nil {
				continue
			}
			totalCount += count
			if totalCount > 500 {
				continue
			}
			var r int
			var rs string
			for i := 0; i < count; i++ {
				if size == "tokens" {
					r = 0
					rs = "token"
				} else {
					r, rs = getNewResult(size)
				}
				var diu string
				if oldSize == "6p" {
					diu, err = getDieImageURL(c, oldSize, rs, color)
				} else {
					diu, err = getDieImageURL(c, size, rs, color)
				}
				if err != nil {
					c.Errorf("could not get die image: %v", err)
				}
				dk := dieKey(c, roomKey, int64(i))
				d := Die{
					Size:      size,
					Result:    r,
					ResultStr: rs,
					Key:       dk,
					KeyStr:    dk.Encode(),
					Timestamp: time.Now().Unix(),
					Image:     diu,
					New:       true,
				}
				dice = append(dice, &d)
				keys = append(keys, dk)
			}
		}
	}

	if sizes["label"] != "" {
		lk := dieKey(c, roomKey, int64(len(dice)))
		l := Die{
			ResultStr: sizes["label"],
			Key:       lk,
			KeyStr:    lk.Encode(),
			Timestamp: time.Now().Unix(),
			Image:     fmt.Sprintf("/label?text=%s&color=%s", sizes["label"], color),
			New:       true,
		}
		dice = append(dice, &l)
		keys = append(keys, lk)
	}
	keyStrings := []string{}
	for _, k := range keys {
		keyStrings = append(keyStrings, k.Encode())
	}
	_, err := datastore.PutMulti(c, keys, dice)
	if err != nil {
		return fmt.Errorf("could not create new dice: %v", err)
	}
	return nil
}

func getRoomDice(c appengine.Context, encodedRoomKey string) ([]Die, error) {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return nil, fmt.Errorf("getRoomDice: could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).Order("Result") //.Limit(10)
	dice := []Die{}
	if _, err = q.GetAll(c, &dice); err != nil {
		return nil, fmt.Errorf("problem executing dice query: %v", err)
	}
	return dice, nil
}

func clearRoomDice(c appengine.Context, encodedRoomKey string) error {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return fmt.Errorf("clearRoomDice: could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).KeysOnly()
	out := q.Run(c)
	for {
		d, err := out.Next(nil)
		if err != nil {
			break
		}
		// TODO(shanel): Refactor to use DeleteMulti
		err = datastore.Delete(c, d)
		if err != nil {
			return fmt.Errorf("problem deleting room dice from room %v: %v", encodedRoomKey, err)
		}
	}
	return nil
}

func getDieImageURL(c appengine.Context, size, result, color string) (string, error) {
	// Fate dice silliness
	ft := map[string]string{"-": "minus", "+": "plus", " ": "zero"}
	if _, ok := ft[result]; ok {
		result = ft[result]
	}
	d := fmt.Sprintf("%s-d%s/%s.png", color, size, result)
	if size == "0" || result == "token" {
		d = fmt.Sprintf("tokens/%s_token.png", color)
	}
	// Should this have a mutex?
	if u, ok := diceURLs[d]; ok {
		return u, nil
	}
	bucket, err := file.DefaultBucketName(c)
	if err != nil {
		return "", fmt.Errorf("failed to get default GCS bucket name: %v", err)
	}
	path := fmt.Sprintf("https://storage.googleapis.com/%v/die_images/%s", bucket, d)
	diceURLs[d] = path
	return path, nil
}

func updateDieLocation(c appengine.Context, encodedDieKey, fp string, x, y float64) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	if err = datastore.Get(c, k, &d); err != nil {
		return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
	}
	d.updatePosition(x, y)
	_, err = datastore.Put(c, k, &d)
	if err != nil {
		return fmt.Errorf("could not update die %v with new position: %v", encodedDieKey, err)
	}
	updateRoom(c, k.Parent().Encode(), Update{Updater: fp, Timestamp: time.Now().Unix()})
	return nil
}

func deleteDieHelper(c appengine.Context, encodedDieKey, fp string) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	if err = datastore.Get(c, k, &d); err != nil {
		return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
	}
	err = datastore.Delete(c, k)
	if err != nil {
		return fmt.Errorf("problem deleting room die %v: %v", encodedDieKey, err)
	}
	// Fake updater so Safari will work?
	updateRoom(c, k.Parent().Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix()})
	return nil
}

func fateReplace(in string) string {
	ft := map[string]string{"-": "minus", "+": "plus", " ": "zero"}
	if r, ok := ft[in]; ok {
		return r
	}
	return in
}

func rerollDieHelper(c appengine.Context, encodedDieKey, fp string) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	if err = datastore.Get(c, k, &d); err != nil {
		return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
	}
	if d.Size == "0" || d.ResultStr == "token" {
		return nil
	}
	oldResultStr := fateReplace(d.ResultStr)
	d.Result, d.ResultStr = getNewResult(d.Size)
	d.Image = strings.Replace(d.Image, fmt.Sprintf("%s.png", oldResultStr), fmt.Sprintf("%s.png", fateReplace(d.ResultStr)), 1)
	_, err = datastore.Put(c, k, &d)
	if err != nil {
		return fmt.Errorf("problem rerolling room die %v: %v", encodedDieKey, err)
	}
	// Fake updater so Safari will work?
	updateRoom(c, k.Parent().Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix()})
	return nil
}

func getNewResult(kind string) (int, string) {
	s, err := strconv.Atoi(kind)
	if err != nil {
		// Assume fate die
		r := rand.Intn(3)
		if r == 0 {
			return math.MaxInt16 - 2, "-"
		}
		if r == 1 {
			return math.MaxInt16, "+"
		}
		return math.MaxInt16 - 1, " "
	}
	r := rand.Intn(s) + 1
	return r, strconv.Itoa(r)
}

func init() {
	http.HandleFunc("/", root)
	http.HandleFunc("/about", about)
	http.HandleFunc("/room", room)
	http.HandleFunc("/room/", room)
	http.HandleFunc("/room/*", room)
	http.HandleFunc("/roll", roll)
	http.HandleFunc("/clear", clear)
	http.HandleFunc("/label", label)
	http.HandleFunc("/move", move)
	http.HandleFunc("/refresh", refresh)
	http.HandleFunc("/delete", deleteDie)
	http.HandleFunc("/reroll", rerollDie)
	rand.Seed(int64(time.Now().Unix()))
}

func root(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err == nil {
		http.Redirect(w, r, fmt.Sprintf("/room/%v", roomCookie.Value), http.StatusFound)
	}
	// If no cookie, then create a room, set cookie, and redirect
	room, err := newRoom(c)
	if err != nil {
		// TODO(shanel): This should probably say something more...
		c.Errorf("no room from root: %v", err)
		http.NotFound(w, r)
	}
	http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room})
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func refresh(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr, err := getEncodedRoomKeyFromName(c, r.Form.Get("id"))
	if err != nil {
		c.Infof("roomname wonkiness in refresh: %v", err)
	}
	fp := r.Form.Get("fp")
	ref := refreshRoom(c, keyStr, fp)
	fmt.Fprintf(w, "%v", ref)
}

func getXY(c appengine.Context, keyStr string, r *http.Request) (float64, float64) {
	x, err := strconv.ParseFloat(r.Form.Get("x"), 64)
	if err != nil {
		c.Errorf("quietly not updating position of %v: %v", keyStr, err)
	}
	y, err := strconv.ParseFloat(r.Form.Get("y"), 64)
	if err != nil {
		c.Errorf("quietly not updating position of %v: %v", keyStr, err)
	}
	return x, y
}

func move(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr, err := getEncodedRoomKeyFromName(c, r.Form.Get("id"))
	if err != nil {
		c.Infof("roomname wonkiness in move: %v", err)
	}
	fp := r.Form.Get("fp")
	x, y := getXY(c, keyStr, r)
	err = updateDieLocation(c, keyStr, fp, x, y)
	if err != nil {
		c.Errorf("quietly not updating position of %v to (%v, %v): %v", keyStr, x, y, err)
	}
	room := path.Base(r.Referer())
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func roll(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		c.Infof("roomname wonkiness in roll: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	toRoll := map[string]string{
		"4":      r.FormValue("d4"),
		"6":      r.FormValue("d6"),
		"6p":     r.FormValue("d6p"),
		"8":      r.FormValue("d8"),
		"10":     r.FormValue("d10"),
		"12":     r.FormValue("d12"),
		"20":     r.FormValue("d20"),
		"F":      r.FormValue("dF"),
		"label":  r.FormValue("label"),
		"tokens": r.FormValue("tokens"),
	}
	color := r.FormValue("color")
	if err = newRoll(c, toRoll, roomKey, color); err != nil {
		c.Errorf("%v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fp := r.FormValue("fp")
	updateRoom(c, roomKey.Encode(), Update{Updater: fp, Timestamp: time.Now().Unix()})
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func deleteDie(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr, err := getEncodedRoomKeyFromName(c, r.Form.Get("id"))
	if err != nil {
		c.Infof("roomname wonkiness in deleteDie: %v", err)
	}
	fp := r.Form.Get("fp")
	room := path.Base(r.Referer())
	// Do we need to be worried dice will be deleted from other rooms?
	err = deleteDieHelper(c, keyStr, fp)
	if err != nil {
		c.Errorf("%v", err)
		http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func rerollDie(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr, err := getEncodedRoomKeyFromName(c, r.Form.Get("id"))
	if err != nil {
		c.Infof("roomname wonkiness in rerollDie: %v", err)
	}
	fp := r.Form.Get("fp")
	room := path.Base(r.Referer())
	// Do we need to be worried dice will be rerolld from other rooms?
	err = rerollDieHelper(c, keyStr, fp)
	if err != nil {
		c.Errorf("%v", err)
		http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func clear(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		c.Infof("roomname wonkiness in clear: %v", err)
	}
	err = clearRoomDice(c, keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fp := r.Form.Get("fp")
	updateRoom(c, keyStr, Update{Updater: fp, Timestamp: time.Now().Unix()})
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func room(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	room := path.Base(r.URL.Path)
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		c.Infof("room wonkiness in room: %v", err)
	}
	dice, err := getRoomDice(c, keyStr)
	if err != nil {
		newRoom, err := newRoom(c)
		if err != nil {
			c.Errorf("no room because: %v", err)
			// TODO(shanel): This should probably say something more...
			http.NotFound(w, r)
		}
		http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: newRoom})
		time.Sleep(100 * time.Nanosecond) // Getting into a race I think...
		http.Redirect(w, r, fmt.Sprintf("/room/%v", newRoom), http.StatusFound)
	}

	cookie := &http.Cookie{Name: "dice_room", Value: room}
	http.SetCookie(w, cookie)
	p := Passer{Dice: dice}
	if err := roomTemplate.Execute(w, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func about(w http.ResponseWriter, r *http.Request) {
	out := (`<html>
	  <body>
	    <center>
	      <p>This is a dice roller. Give the URL of the room to others and they can see and do everything you can see and do.</p>
        <p>Site based on (and dice images borrowed from) <a href="https://www.thievesoftime.com/">Graham Walmsley</a>'s <a href="//https://catchyourhare.com/diceroller/">dice roller</a>.</p>
	      <p<a href="http://story-games.com/forums/discussion/comment/276305/#Comment_276305">Roll Dice Or Say Yes</a>.</p>
	      <p>The token image is "coin by Arthur Shlain from the Noun Project."</p>
	      <p>Hex conversion code borrowed from <a href="https://github.com/dlion/hex2rgb">here</a>.</p>
	      <p>The code is available <a href="https://github.com/shanel/roller">here</a>.</p>
	      <p>Bugs or feature requests should go <a href="https://github.com/shanel/roller/issues">here</a>.</p>
	    </center>
	  </body>
	</html>`)
	fmt.Fprintf(w, "%s", out)
}

func Convert(h string) color.RGBA {

	if strings.HasPrefix(h, "#") {
		h = strings.Replace(h, "#", "", 1)
	}

	if len(h) == 3 {
		h = fmt.Sprintf("%c%c%c%c%c%c", h[0], h[0], h[1], h[1], h[2], h[2])
	}

	d, _ := hex.DecodeString(h)

	return color.RGBA{uint8(d[0]), uint8(d[1]), uint8(d[2]), uint8(1)}
}

func pngtest(w http.ResponseWriter, r *http.Request) {
}

func label(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	text, err := url.QueryUnescape(r.URL.Query()["text"][0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	col := r.URL.Query()["color"][0]

	// Read the font data.
	//	fontBytes, err := ioutil.ReadFile("luximr.ttf")
	fontBytes, err := ioutil.ReadFile("Roboto-Regular.ttf")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	f, err := freetype.ParseFont(fontBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	cols := map[string]string{
		"clear":  "ffffff",
		"blue":   "1e90ff",
		"green":  "008b45",
		"orange": "ff8c00",
		"red":    "ff3333",
		"violet": "8a2be2",
		"gold":   "ffd700",
	}
	if _, ok := cols[col]; !ok {
		c.Errorf("couldn't find color %s", col)
		http.Error(w, fmt.Sprintf("couldn't find color %s", col), http.StatusInternalServerError)
	}
	// Initialize the context.
	//fg, bg := image.NewUniform(Convert(cols[col])), image.Black
	fg, bg := image.Black, image.Opaque
	rc := utf8.RuneCountInString(text)
	if (rc % 2) == 0 {
		rc += 1
	}
	width := (int(math.Ceil((float64(rc)*float64(18))/float64(72))) * 52) // + 10
	rgba := image.NewRGBA(image.Rect(0, 0, width, 48))
	draw.Draw(rgba, rgba.Bounds(), bg, image.ZP, draw.Src)
	fc := freetype.NewContext()
	fc.SetDPI(96)
	fc.SetFont(f)
	fc.SetFontSize(18)
	fc.SetClip(rgba.Bounds())
	fc.SetDst(rgba)
	fc.SetSrc(fg)
	fc.SetHinting(font.HintingNone)

	// Draw the text.
	pt := freetype.Pt(10, 10+int(fc.PointToFixed(18)>>6))
	_, err = fc.DrawString(text, pt)
	if err != nil {
		log.Println(err)
		return
	}
	pt.Y += fc.PointToFixed(18 * 1.5)

	err = png.Encode(w, rgba)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	c.Infof("Wrote out png OK.")
}

var roomTemplate = template.Must(template.New("room").Parse(`
<html>

<head>
    <title>Roll For Your Party</title>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/interact.js/1.2.9/interact.js"></script>
    <script src="https://ajax.googleapis.com/ajax/libs/jquery/3.2.1/jquery.min.js"></script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/fingerprintjs2/1.5.1/fingerprint2.min.js"></script>

<!--    <a href="https://github.com/shanel/roller" class="github-corner" aria-label="View source on Github"><svg width="80" height="80" viewBox="0 0 250 250" style="fill:#fff; color:#151513; position: absolute; top: 0; border: 0; right: 0;" aria-hidden="true"><path d="M0,0 L115,115 L130,115 L142,142 L250,250 L250,0 Z"></path><path d="M128.3,109.0 C113.8,99.7 119.0,89.6 119.0,89.6 C122.0,82.7 120.5,78.6 120.5,78.6 C119.2,72.0 123.4,76.3 123.4,76.3 C127.3,80.9 125.5,87.3 125.5,87.3 C122.9,97.6 130.6,101.9 134.4,103.2" fill="currentColor" style="transform-origin: 130px 106px;" class="octo-arm"></path><path d="M115.0,115.0 C114.9,115.1 118.7,116.5 119.8,115.4 L133.7,101.6 C136.9,99.2 139.9,98.4 142.2,98.6 C133.8,88.0 127.5,74.4 143.8,58.0 C148.5,53.4 154.0,51.2 159.7,51.0 C160.3,49.4 163.2,43.6 171.4,40.1 C171.4,40.1 176.1,42.5 178.8,56.2 C183.1,58.6 187.2,61.8 190.9,65.4 C194.5,69.0 197.7,73.2 200.1,77.6 C213.8,80.2 216.3,84.9 216.3,84.9 C212.7,93.1 206.9,96.0 205.4,96.6 C205.1,102.4 203.0,107.8 198.3,112.5 C181.9,128.9 168.3,122.5 157.7,114.1 C157.9,116.9 156.7,120.9 152.7,124.9 L141.0,136.5 C139.8,137.7 141.6,141.9 141.8,141.8 Z" fill="currentColor" class="octo-body"></path></svg></a><style>.github-corner:hover .octo-arm{animation:octocat-wave 560ms ease-in-out}@keyframes octocat-wave{0%,100%{transform:rotate(0)}20%,60%{transform:rotate(-25deg)}40%,80%{transform:rotate(10deg)}}@media (max-width:500px){.github-corner:hover .octo-arm{animation:none}.github-corner .octo-arm{animation:octocat-wave 560ms ease-in-out}}</style> -->

    <a href="https://github.com/shanel/roller" class="github-corner" aria-label="View source on Github"><svg width="80" height="80" viewBox="0 0 250 250" style="fill:#151513; color:#fff; position: absolute; top: 0; border: 0; right: 0;" aria-hidden="true"><path d="M0,0 L115,115 L130,115 L142,142 L250,250 L250,0 Z"></path><path d="M128.3,109.0 C113.8,99.7 119.0,89.6 119.0,89.6 C122.0,82.7 120.5,78.6 120.5,78.6 C119.2,72.0 123.4,76.3 123.4,76.3 C127.3,80.9 125.5,87.3 125.5,87.3 C122.9,97.6 130.6,101.9 134.4,103.2" fill="currentColor" style="transform-origin: 130px 106px;" class="octo-arm"></path><path d="M115.0,115.0 C114.9,115.1 118.7,116.5 119.8,115.4 L133.7,101.6 C136.9,99.2 139.9,98.4 142.2,98.6 C133.8,88.0 127.5,74.4 143.8,58.0 C148.5,53.4 154.0,51.2 159.7,51.0 C160.3,49.4 163.2,43.6 171.4,40.1 C171.4,40.1 176.1,42.5 178.8,56.2 C183.1,58.6 187.2,61.8 190.9,65.4 C194.5,69.0 197.7,73.2 200.1,77.6 C213.8,80.2 216.3,84.9 216.3,84.9 C212.7,93.1 206.9,96.0 205.4,96.6 C205.1,102.4 203.0,107.8 198.3,112.5 C181.9,128.9 168.3,122.5 157.7,114.1 C157.9,116.9 156.7,120.9 152.7,124.9 L141.0,136.5 C139.8,137.7 141.6,141.9 141.8,141.8 Z" fill="currentColor" class="octo-body"></path></svg></a><style>.github-corner:hover .octo-arm{animation:octocat-wave 560ms ease-in-out}@keyframes octocat-wave{0%,100%{transform:rotate(0)}20%,60%{transform:rotate(-25deg)}40%,80%{transform:rotate(10deg)}}@media (max-width:500px){.github-corner:hover .octo-arm{animation:none}.github-corner .octo-arm{animation:octocat-wave 560ms ease-in-out}}</style>

    <script type="text/javascript" language="javascript">

        var fp = "";
        new Fingerprint2().get(function(result, components) {
            fp = result; //a hash, representing your device fingerprint
            var x = document.getElementsByName("fp");
            x[0].value = fp;
        });

        // target elements with the "draggable" class
        interact('.draggable')
            .draggable({
                // enable inertial throwing
                inertia: true,
                // keep the element within the area of it's parent
                //    restrict: {
                //      restriction: "parent",
                //      endOnly: true,
                //      elementRect: { top: 0, left: 0, bottom: 1, right: 1 }
                //    },
                // enable autoScroll
                autoScroll: true,

                onstart: function(event) {
                    // update the posiion attributes
                    event.target.setAttribute('start-x', event.x0);
                    event.target.setAttribute('start-y', event.y0);
                },
                // call this function on every dragmove event
                onmove: dragMoveListener,
                onend: dragMoveEnd,
                // call this function on every dragend event
            });


        function dragMoveEnd(event) {
            var target = event.target,
                // keep the dragged position in the data-x/data-y attributes
                x = (parseFloat(target.getAttribute('data-x')) || 0),
                y = (parseFloat(target.getAttribute('data-y')) || 0);


	    // TODO(shanel): This is janky and annoying...
            var transformer = target.style.transform;
            if (transformer.search("px") != -1) {
                x += parseFloat(target.getAttribute('start-x'));
                y += parseFloat(target.getAttribute('start-y'));
            }


            // translate the element
	    target.removeAttribute("style");
            target.style.position = 'absolute';
            target.style.top = y + 'px';
            target.style.left = x + 'px';

            // update the position attributes
            target.setAttribute('data-x', x);
            target.setAttribute('data-y', y);

            $.post('/move', {
                'id': target.id,
                'x': x,
                'y': y,
                'fp': fp
            });

        }

        var delete_cookie = function(name) {
           document.cookie = name + '=; expires=Thu, 01 Jan 1970 00:00:01 GMT; path=/';
           document.cookie = name + '=; expires=Thu, 01 Jan 1970 00:00:01 GMT; path=/room';
        };

        var getNewRoom = function() {
           delete_cookie("dice_room");
           window.location.replace("/");
        };


        function dragMoveListener(event) {
            var target = event.target,
                // keep the dragged position in the data-x/data-y attributes
                x = (parseFloat(target.getAttribute('data-x')) || 0) + event.dx,
                y = (parseFloat(target.getAttribute('data-y')) || 0) + event.dy;



            // translate the element
            var transformer = target.style.transform;
            if (transformer.search("px") != -1) {
                target.style.mstransform =
                target.style.webkittransform =
                    target.style.transform =
                    'translate(' + x + 'px, ' + y + 'px)';
            } else {
                target.style = null;
                target.style.position = 'absolute';
                target.style.top = y + 'px';
                target.style.left = x + 'px';
            }


            // update the posiion attributes
            target.setAttribute('data-x', x);
            target.setAttribute('data-y', y);
        }


        // this is used later in the resizing and gesture demos
        window.dragMoveListener = dragMoveListener;


        function deleteMarked() {
            var toDelete = document.getElementsByClassName("selected");
            for (var i = 0; i < toDelete.length; i++) {
                $.post("/delete", {
                    id: toDelete[i].id,
                    'fp': fp
                }).done(function(data) {});
            }
            if (toDelete.length > 0) {
                $("#refreshable").load(window.location.href + " #refreshable");
            }
            $("#refreshable").load(window.location.href + " #refreshable");
        }

        function rerollMarked() {
            var toReroll = document.getElementsByClassName("selected");
            for (var i = 0; i < toReroll.length; i++) {
                $.post("/reroll", {
                    id: toReroll[i].id,
                    'fp': fp
                }).done(function(data) {});
            }
            if (toReroll.length > 0) {
                $("#refreshable").load(window.location.href + " #refreshable");
            }
            $("#refreshable").load(window.location.href + " #refreshable");
        }

        function clearAllDice() {
            $.post("/clear", {
                'fp': fp
            }).done(function(data) {});
            $("#refreshable").load(window.location.href + " #refreshable");
        }

        interact('.tap-target')
            .on('tap', function(event) {
                event.currentTarget.classList.toggle('selected');
                //    event.preventDefault();
            });


        function autoRefresh_div() {
            var room = (window.location.pathname).split("/")[2];
            $.post("/refresh", {
                    id: room,
                    fp: fp,
                })
                .done(function(data) {
                    var b = data;
                    if (b != "") {
                        if (sessionStorage.lastUpdateId) {
                            if (b != sessionStorage.lastUpdateId) {
                                $("#refreshable").load(window.location.href + " #refreshable");
                                sessionStorage.lastUpdateId = b;
                            }
                        } else {
                            $("#refreshable").load(window.location.href + " #refreshable");
                            sessionStorage.lastUpdateId = b;
                        }
                    }
                });
        }

        setInterval('autoRefresh_div()', 1000); // refresh div after 1 second
    </script>
</head>

<style type="text/css">
body {
	color:#000000;
	background-color:#ffffff;
}

.tap-target {
  display: inline-block;
}

.tap-target.selected {
  background-color: #f40;
  border-style: solid;
  border-color: red;
}


html {
  height: 100%;
  box-sizing: border-box;
}

*,
*:before,
*:after {
  box-sizing: inherit;
}

body {
  position: relative;
  margin: 0;
  padding-bottom: 6rem;
  min-height: 100%;
  font-family: "Helvetica Neue", Arial, sans-serif;
}

.footer {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  padding: 1rem;
  background-color: #efefef;
  text-align: center;
}

.attribution {
	font-size: x-small;
}

button {
  background-color: #e7e7e7;
  color: black;
  padding: 5px 5px;
  text-align: center;
  text-decoration: none;
  display: inline-block;
  margin: 4px 2px;
  cursor: pointer;
  border-radius: 8px;
}

.button2 { background-color: #f44336; color: white; }

/* Tooltip container */
.tooltip {
    position: relative;
    display: inline-block;
    border-bottom: 1px dotted black; /* If you want dots under the hoverable text */
}

/* Tooltip text */
.tooltip .tooltiptext {
    visibility: hidden;
    width: 120px;
    background-color: black;
    color: #fff;
    text-align: center;
    padding: 5px 0;
    border-radius: 6px;
 
    /* Position the tooltip text - see examples below! */
    position: absolute;
    z-index: 1;
}

/* Show the tooltip text when you mouse over the tooltip container */
.tooltip:hover .tooltiptext {
    visibility: visible;
}

</style>

<body>
    <center>
        <h3>Roll For Your Party: A multi-user dice roller.</h3>
        <p>Send the URL to your friends! Drag the dice around! Click on dice to select/unselect them!</p>
        <form id="rollem" action="/roll" method="post">
            d4: <input type="text" name="d4" style="width: 19px"></input>
             d6: <input type="text" name="d6" style="width: 19px"></input>
             <div class="tooltip">d6(P): <span class="tooltiptext">d6 with pips</span></div> <input type="text" name="d6p" style="width: 19px"></input>
             d8: <input type="text" name="d8" style="width: 19px"></input>
             d10: <input type="text" name="d10" style="width: 19px"></input>
             d12: <input type="text" name="d12" style="width: 19px"></input>
             d20: <input type="text" name="d20" style="width: 19px"></input>
             dF: <input type="text" name="dF" style="width: 19px"></input>
             tokens: <input type="text" name="tokens" style="width: 19px"></input>
             label: <input type="text" name="label" style="width: 100"></input>

             color: <select id="selectColor" name="color">
          			<option value="clear" style="color: #ffffff" >Clear</option>
		            <option value="blue" style="color: #1e90ff">Blue</option>
          			<option value="green" style="color: #008b45">Green</option>
          			<option value="orange" style="color: #ff8c00">Orange</option>
          			<option value="red" style="color: #ff3333">Red</option>
          			<option value="violet" style="color: #8a2be2" >Purple</option>
          			<option value="gold" style="color: #ffd700" >Yellow</option>
         		</select>

            <input type="hidden" name="fp" value="">
            <p></p>
        </form>
        <button class="button button2" form="rollem" formaction="/roll" formmethod="post">Submit      </button>
        <button class="button" onclick="clearAllDice()">Clear</button>
        <button class="button" onclick="deleteMarked()">Delete selected</button>
        <button class="button" onclick="rerollMarked()">Reroll selected</button>
        <button class="button" onclick="getNewRoom()">New room</button> 
    <br>
    <a href="/about">about</a>
    </center>
    <hr>
    <center>
        <div id="refreshable">
            {{range .Dice}} {{if .New}}
            <div id="{{.KeyStr}}" class="draggable tap-target" data-x="{{.X}}" data-y="{{.Y}}" style="transform: translate({{.X}}px, {{.Y}}px)" ;>
                <img src="{{.Image}}" alt="d{{.Size}}: {{.ResultStr}}">
            </div>
            {{else}}
            <div id="{{.KeyStr}}" class="draggable tap-target" data-x="{{.X}}" data-y="{{.Y}}" style="position: absolute; left: {{.X}}px; top: {{.Y}}px;">
                <img src="{{.Image}}" alt="d{{.Size}}: {{.ResultStr}}">
            </div>
            {{end}} {{end}}
        </div>
    </center>
<div class="footer">
    If you get use out of this site, please consider donating to <a href="http://www.shantibhavanchildren.org/">Shanti Bhavan</a>.
		<br>
		<div class="attribution">
		Design and functionality based on <a href="https://www.thievesoftime.com/">Graham Walmsley</a>'s <a href="//https://catchyourhare.com/diceroller/">dice roller</a>.
		</div>
</div>
</body>

</html>
`))
