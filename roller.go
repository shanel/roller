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
package roller

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
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
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dustinkirkland/golang-petname"
	"github.com/golang/freetype"
	"golang.org/x/net/context"
	"golang.org/x/image/font"

	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/file"
	// Maybe use this later?
	//"appengine/user"
)

// As we create urls for the die images, store them here so we don't keep making them
var diceURLs = map[string]string{}
var refreshDelta = int64(2)

type Update struct {
	Timestamp int64
	Updater   string
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

func getEncodedRoomKeyFromName(c context.Context, name string) (string, error) {
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

func updateRoom(c context.Context, rk string, u Update) error {
	roomKey, err := datastore.DecodeKey(rk)
	if err != nil {
		return fmt.Errorf("updateRoom: could not decode room key %v: %v", rk, err)
	}
	var r Room
	t := time.Now().Unix()
	if err = datastore.Get(c, roomKey, &r); err != nil {
		// Couldn't find it, so create it
		log.Printf("couldn't find room %v, so going to create it", rk)
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

func refreshRoom(c context.Context, rk, fp string) string {
	roomKey, err := datastore.DecodeKey(rk)
	out := ""
	if err != nil {
		log.Printf("refreshRoom: could not decode room key %v: %v", rk, err)
		return out
	}
	var r Room
	if err = datastore.Get(c, roomKey, &r); err != nil {
		log.Printf("could not find room %v for refresh: %v", rk, err)
		return out
	}
	keep := []Update{}
	now := time.Now().Unix()
	var umUpdates []Update
	var send []Update
	err = json.Unmarshal(r.Updates, &umUpdates)
	if err != nil {
		log.Printf("could not unmarshal updates in refreshRoom: %v", err)
		return ""
	}
	for _, u := range umUpdates {
		q := now - u.Timestamp
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
		log.Printf("could not marshal updates in refreshRoom: %v", err)
		return ""
	}
	_, err = datastore.Put(c, roomKey, &r)
	if err != nil {
		log.Printf("could not create updated room %v: %v", rk, err)
	}
	if len(send) > 0 {
		toHash, err := json.Marshal(send)
		if err != nil {
			log.Printf("could not marshal updates to send in refreshRoom: %v", err)
			return ""
		}
		out = fmt.Sprintf("%x", md5.Sum(toHash))
	}
	return out
}

// roomKey creates a new room entity key.
func roomKey(c context.Context) *datastore.Key {
	return datastore.NewKey(c, "Room", "", time.Now().UnixNano(), nil)
}

// dieKey creates a new die entity key.
func dieKey(c context.Context, roomKey *datastore.Key, i int64) *datastore.Key {
	return datastore.NewKey(c, "Die", "", time.Now().UnixNano()+i, roomKey)
}

func newRoom(c context.Context) (string, error) {
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

func newRoll(c context.Context, sizes map[string]string, roomKey *datastore.Key, color string) error {
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
					log.Printf("could not get die image: %v", err)
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

func getRoomDice(c context.Context, encodedRoomKey string) ([]Die, error) {
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

func clearRoomDice(c context.Context, encodedRoomKey string) error {
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

func getDieImageURL(c context.Context, size, result, color string) (string, error) {
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
	p := fmt.Sprintf("https://storage.googleapis.com/%v/die_images/%s", bucket, d)
	diceURLs[d] = p
	return p, nil
}

func updateDieLocation(c context.Context, encodedDieKey, fp string, x, y float64) error {
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

func deleteDieHelper(c context.Context, encodedDieKey string) error {
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

func rerollDieHelper(c context.Context, encodedDieKey string) error {
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
	http.HandleFunc("/paused", paused)
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
		log.Printf("no room from root: %v", err)
		http.NotFound(w, r)
	}
	http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room})
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

// TODO(shanel): Maybe the room/die culling code should happen here? Also, I wonder if it should
// instead of go to a separate page, just have a butter bar saying it is only updating every hour
// or something and update the JS accordingly.
func paused(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	out := "<html><center>To save on bandwidth we have stopped updating you since you have been idle for an hour. To get back to your room, click <a href=\"/room/%v\">here</a>.</center></html>"
	fmt.Fprintf(w, out, r.Form.Get("id"))
}

func refresh(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr, err := getEncodedRoomKeyFromName(c, r.Form.Get("id"))
	if err != nil {
		log.Printf("roomname wonkiness in refresh: %v", err)
	}
	fp := r.Form.Get("fp")
	ref := refreshRoom(c, keyStr, fp)
	fmt.Fprintf(w, "%v", ref)
}

func getXY(keyStr string, r *http.Request) (float64, float64) {
	x, err := strconv.ParseFloat(r.Form.Get("x"), 64)
	if err != nil {
		log.Printf("quietly not updating position of %v: %v", keyStr, err)
	}
	y, err := strconv.ParseFloat(r.Form.Get("y"), 64)
	if err != nil {
		log.Printf("quietly not updating position of %v: %v", keyStr, err)
	}
	return x, y
}

func move(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr := r.Form.Get("id")
	fp := r.Form.Get("fp")
	x, y := getXY(keyStr, r)
	err := updateDieLocation(c, keyStr, fp, x, y)
	if err != nil {
		log.Printf("quietly not updating position of %v to (%v, %v): %v", keyStr, x, y, err)
	}
	room := path.Base(r.Referer())
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func roll(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in roll: %v", err)
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
	col := r.FormValue("color")
	if err = newRoll(c, toRoll, roomKey, col); err != nil {
		log.Printf("%v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fp := r.FormValue("fp")
	updateRoom(c, roomKey.Encode(), Update{Updater: fp, Timestamp: time.Now().Unix()})
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func deleteDie(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr := r.Form.Get("id")
	room := path.Base(r.Referer())
	// Do we need to be worried dice will be deleted from other rooms?
	err := deleteDieHelper(c, keyStr)
	if err != nil {
		log.Printf("%v", err)
		http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func rerollDie(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr := r.Form.Get("id")
	room := path.Base(r.Referer())
	// Do we need to be worried dice will be rerolled from other rooms?
	err := rerollDieHelper(c, keyStr)
	if err != nil {
		log.Printf("%v", err)
		http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	http.Redirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func clear(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in clear: %v", err)
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
		log.Printf("room wonkiness in room: %v", err)
	}
	dice, err := getRoomDice(c, keyStr)
	if err != nil {
		newRoom, err := newRoom(c)
		if err != nil {
			log.Printf("no room because: %v", err)
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
	content, err := ioutil.ReadFile("room.tmpl.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	roomTemplate := template.Must(template.New("room").Parse(string(content[:])))
	if err := roomTemplate.Execute(w, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func about(w http.ResponseWriter, _ *http.Request) {
	if out, err := ioutil.ReadFile("about.html"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", out)
	}
}

func label(w http.ResponseWriter, r *http.Request) {
	text, err := url.QueryUnescape(r.URL.Query()["text"][0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	col := r.URL.Query()["color"][0]

	// Read the font data.
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
		log.Printf("couldn't find color %s", col)
		http.Error(w, fmt.Sprintf("couldn't find color %s", col), http.StatusInternalServerError)
	}
	// Initialize the context.
	//fg, bg := image.NewUniform(Convert(cols[col])), image.Black
	fg, bg := image.Black, image.Opaque
	rc := utf8.RuneCountInString(text)
	if (rc % 2) == 0 {
		rc += 1
	}
	width := int(math.Ceil((float64(rc)*float64(18))/float64(72))) * 52
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
}

