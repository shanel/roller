//    AppEngine based Dice Roller
//    Copyright (C) 2017 Shane Liebling
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

// TODO(shanel): Need to clean up the order of this file, move the js into its own file, nuke useless comments, write tests...
package main

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"github.com/adamclerk/deck"
	"github.com/beevik/etree"
	"github.com/dustinkirkland/golang-petname"
	"os"

	"cloud.google.com/go/datastore"
	"html/template"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// TODO(shanel): Audit all the RunInTransaction calls and pass ReadOnly if only Get or GetMulti are happening.

// If you have forked this and are running it yourself you might need to change this.
const bucket = "dice-roller-174222.appspot.com"

var (
	// As we create urls for the die images, store them here so we don't keep making them
	diceURLs     = map[string]string{}
	refreshDelta = int64(2)
	lastRoll     = map[string]int{}
	lastAction   = map[string]string{}
	// Keep track of attempts to hit non-existent rooms and only create a new room once
	repeatOffenders = map[string]bool{}
	cardToPNG       = map[string]string{
		"A♣": "ace_of_clubs.png", "2♣": "2_of_clubs.png", "3♣": "3_of_clubs.png", "4♣": "4_of_clubs.png", "5♣": "5_of_clubs.png", "6♣": "6_of_clubs.png", "7♣": "7_of_clubs.png", "8♣": "8_of_clubs.png", "9♣": "9_of_clubs.png", "T♣": "10_of_clubs.png", "J♣": "jack_of_clubs.png", "Q♣": "queen_of_clubs.png", "K♣": "king_of_clubs.png",
		"A♦": "ace_of_diamonds.png", "2♦": "2_of_diamonds.png", "3♦": "3_of_diamonds.png", "4♦": "4_of_diamonds.png", "5♦": "5_of_diamonds.png", "6♦": "6_of_diamonds.png", "7♦": "7_of_diamonds.png", "8♦": "8_of_diamonds.png", "9♦": "9_of_diamonds.png", "T♦": "10_of_diamonds.png", "J♦": "jack_of_diamonds.png", "Q♦": "queen_of_diamonds.png", "K♦": "king_of_diamonds.png",
		"A♥": "ace_of_hearts.png", "2♥": "2_of_hearts.png", "3♥": "3_of_hearts.png", "4♥": "4_of_hearts.png", "5♥": "5_of_hearts.png", "6♥": "6_of_hearts.png", "7♥": "7_of_hearts.png", "8♥": "8_of_hearts.png", "9♥": "9_of_hearts.png", "T♥": "10_of_hearts.png", "J♥": "jack_of_hearts.png", "Q♥": "queen_of_hearts.png", "K♥": "king_of_hearts.png",
		"A♠": "ace_of_spades.png", "2♠": "2_of_spades.png", "3♠": "3_of_spades.png", "4♠": "4_of_spades.png", "5♠": "5_of_spades.png", "6♠": "6_of_spades.png", "7♠": "7_of_spades.png", "8♠": "8_of_spades.png", "9♠": "9_of_spades.png", "T♠": "10_of_spades.png", "J♠": "jack_of_spades.png", "Q♠": "queen_of_spades.png", "K♠": "king_of_spades.png"}
	faceMap      = map[string]int{"A": 0, "2": 1, "3": 2, "4": 3, "5": 4, "6": 5, "7": 6, "8": 7, "9": 8, "T": 9, "J": 10, "Q": 11, "K": 12}
	suitMap      = map[string]int{"♣": 0, "♦": 1, "♥": 2, "♠": 3}
	previousSVGs = map[string][]byte{}
	dsClient     *datastore.Client
)

type Update struct {
	Timestamp int64
	Updater   string
	UpdateAll bool
	Message   string
}

type Room struct {
	Updates    []byte // hooray having to use json
	Timestamp  int64
	Slug       string
	Deck       string
	BgURL      string
	CustomSets []byte // yup, having to use json again...
	Modifier   int
}

func (r *Room) GetCustomSets() (CustomSets, error) {
	out := CustomSets{}
	if len(r.CustomSets) == 0 {
		return out, nil
	}
	err := json.Unmarshal(r.CustomSets, &out)
	if err != nil {
		return out, fmt.Errorf("could not unmarshal updates in GetCustomSets: %v", err)
	}
	return out, nil
}

func (r *Room) SetCustomSets(cs CustomSets) error {
	toSave, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	r.CustomSets = toSave
	return nil
}

type CustomSets map[string]CustomSet

type CustomSet struct {
	// TODO(shanel): maybe make this use a mutex in the future?
	Template  map[string]string // url map for easy searching
	Instance  map[string]string
	MaxHeight string
	MaxWidth  string
}

func (cs *CustomSet) Draw(c int) (map[string]string, error) {
	left := len(cs.Instance)
	out := map[string]string{}
	if left == 0 {
		return out, fmt.Errorf("the deck is empty")
	}
	if left <= c {
		c = left
	}
	remove := []string{}
	keys := []string{}
	for k := range cs.Instance {
		keys = append(keys, k)
	}
	dest := make([]string, len(keys))
	perm := rand.Perm(len(keys))
	for i, v := range perm {
		dest[v] = keys[i]
	}
	for i := 0; i < c; i++ {
		remove = append(remove, dest[i])
	}
	for _, k := range remove {
		out[k] = cs.Instance[k]
		delete(cs.Instance, k)
	}
	return out, nil
}

func (cs *CustomSet) shuffleDiscards(stillOut map[string]bool) {
	newInstance := map[string]string{}
	for k, v := range cs.Template {
		if _, ok := stillOut[k]; !ok {
			newInstance[k] = v
		}
	}
	cs.Instance = newInstance
}

type PassedCustomSet struct {
	Remaining int
	Name      string
	SnakeName string
	Pull      template.JS
	Randomize template.JS
	Remove    template.JS
	Height    template.JS
	Width     template.JS
}

func smartRedirect(w http.ResponseWriter, r *http.Request, url string, code int) {
	if !strings.Contains(r.Host, "localhost") {
		// If this isn't localhost, always redirect to https.
		url = fmt.Sprintf("https://%s%s", r.Host, url)
	}
	http.Redirect(w, r, url, code)
}

func newCustomSetFromNewlineSeparatedString(u, height, width string) (CustomSet, error) {
	// Get rid of random space at front or end
	u = strings.TrimSpace(u)
	// This will make single item lists work
	if !strings.Contains(u, "\n") {
		u += "\n"
	}
	pieces := strings.Split(u, "\n")
	slimPieces := []string{}
	for _, piece := range pieces {
		if piece != "" {
			slimPieces = append(slimPieces, piece)
		}
	}
	cs := CustomSet{Template: map[string]string{}, Instance: map[string]string{}, MaxHeight: height, MaxWidth: width}
	for i, p := range slimPieces {
		si := strconv.Itoa(i)
		cs.Template[si] = p
		cs.Instance[si] = p
	}
	return cs, nil
}

func createSVG(die, result, color string) ([]byte, error) {
	key := fmt.Sprintf("%s-%s-%s", die, result, color)
	if found, ok := previousSVGs[key]; ok {
		return found, nil
	}
	var p string
	if die == "dH" {
		if result == "0" {
			p = fmt.Sprintf("https://storage.googleapis.com/%v/die_images/dH-blank.svg", bucket)
		} else {
			p = fmt.Sprintf("https://storage.googleapis.com/%v/die_images/dH-x.svg", bucket)
		}
	} else {
		p = fmt.Sprintf("https://storage.googleapis.com/%v/die_images/%s.svg", bucket, die)
	}
	res, err := http.Get(p)
	if err != nil {
		log.Printf("could not get svg: %v", err)
		return nil, err
	}
	defer func() {
		err := res.Body.Close()
		if err != nil {
			log.Printf("error closing body: %v", err)
		}
	}()
	slurp, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Printf("issue reading svg: %v", err)
		return nil, err
	}
	colors := map[string]string{
		"clear":     "rgb(228, 242, 247)", // #e4f2f7
		"green":     "rgb(131, 245, 108)", // #83f56c
		"red":       "rgb(228, 79, 79)",   // #e44f4f
		"blue":      "rgb(88, 181, 243)",  // #58b5f3
		"orange":    "rgb(255, 158, 12)",  // #ff9e0c
		"purple":    "rgb(142, 119, 218)", // #8e77da
		"violet":    "rgb(142, 119, 218)", // #8e77da
		"pink":      "rgb(255, 105, 180)", //#ff69b4
		"magenta":   "rgb(255, 0, 255)",   // #ff00ff
		"turquoise": "rgb(64, 224, 208)",  // #40e0d0
		"silver":    "rgb(192, 192, 192)", // #c0c0c0
		"lavender":  "rgb(230, 230, 250)", // #e6e6fa
		"khaki":     "rgb(240, 230, 140)", // #f0e68c
		"gold":      "rgb(254, 248, 78)",  // #fef84e
		"white":     "rgb(255, 255, 255)",
	}
	clr, ok := colors[color]
	if !ok {
		clr = colors["clear"]
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(slurp); err != nil {
		log.Printf("read failed: %v", err)
		return nil, err
	}
	root := doc.SelectElement("svg")
	title := doc.CreateElement("title")
	title.SetText(fmt.Sprintf("%s %s: %s", color, die, result))
	rch := root.ChildElements()
	root.InsertChild(rch[0], title)
	opt := fmt.Sprintf("opt opt-%s", result)
	for _, each := range root.SelectElements("g") {
		gclass := each.SelectAttrValue("class", "")
		if gclass == opt {
			c := each.ChildElements()
			if len(c) != 0 && die != "d6p" {
				c[0].CreateAttr("style", "visibility: visible;")
			} else {
				each.CreateAttr("style", "visibility: visible;")
			}
		} else if gclass == "opt" { // token?
			if result == "1" {
				each.CreateAttr("style", "visibility: visible;")
			} else {
				each.CreateAttr("style", "visibility: hidden;")
			}
		}
		for _, pth := range each.ChildElements() {
			if pth != nil {
				class := pth.SelectAttrValue("class", "")
				if class == "stroke" {
					pth.CreateAttr("style", "fill: rgb(0, 0, 0);")
				} else if class == "fill" {
					pth.CreateAttr("style", fmt.Sprintf("fill: %s;", clr))
				}
			}
		}
		text := each.SelectElement("text")
		if text != nil {
			text.SetText(result)
		}
	}
	doc.Indent(2)
	out, err := doc.WriteToBytes()
	if err == nil {
		previousSVGs[key] = out
	}
	return out, err
}

type Die struct {
	Size          string // for fate dice this won't be an integer
	Result        int    // For fate dice make this one of three very large numbers?
	ResultStr     string
	X             float64
	Y             float64
	Key           *datastore.Key
	KeyStr        string
	Timestamp     int64
	Image         string
	FlippedImage  string
	New           bool
	IsCard        bool
	IsLabel       bool
	IsCustomItem  bool
	CustomSetName string
	CustomHeight  string
	CustomWidth   string
	HiddenBy      string
	IsHidden      bool
	IsFunky       bool
	IsImage       bool
	IsClock       bool
	Color         string
	OldColor      string
	IsFlipped     bool
	Version       int // Use this to determine whether to use old display logic or new
	SVGPath       string
	SVG           template.HTML `datastore:",noindex"`
	IsToken       bool
	SVGBytes      []byte `datastore:",noindex"`
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
	Dice                []Die
	RoomTotal           int
	RoomAvg             float64
	RollTotal           int
	RollAvg             float64
	LastAction          string
	CardsLeft           int
	BgURL               string
	HasBgURL            bool
	CustomSets          []PassedCustomSet
	Modifier            int
	ModifiedRollTotal   int
	TokenCount          int
	LastChangeTimestamp string
}

func noSpaces(str string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, str)
}

func generateRoomName(wc int) string {
	// 3 part words will allow for 91922432 unique names
	// 4 part words will allow for 41181249536 unique names
	name := petname.Generate(wc, " ")
	name = strings.Title(name)
	return noSpaces(name)
}

func getEncodedRoomKeyFromName(c context.Context, name string) (string, error) {
	q := datastore.NewQuery("Room").Filter("Slug =", name).Limit(1).KeysOnly()
	k, err := dsClient.GetAll(c, q, nil)
	if err != nil {
		return name, fmt.Errorf("problem executing room (by Slug) query: %v", err)
	}
	if len(k) > 0 {
		return k[0].Encode(), nil
	}
	return name, fmt.Errorf("couldn't find a room key for %v", name)
}

func updateRoom(c context.Context, rk string, u Update, modifier int) {
	roomKey, err := datastore.DecodeKey(rk)
	if err != nil {
		log.Printf("updateRoom: could not decode room key %v: %v", rk, err)
		return
	}
	var r Room
	t := time.Now().Unix()
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		err = tx.Get(roomKey, &r)
		if err == datastore.ErrNoSuchEntity {
			// Couldn't find it, so create it
			log.Printf("couldn't find room %v, so going to create it", rk)
			up, err := json.Marshal([]Update{})
			if err != nil {
				return fmt.Errorf("could not marshal update: %v", err)
			}

			deck.Seed()
			d, err := deck.New(deck.Unshuffled)
			if err != nil {
				log.Printf("could not create deck: %v", err)
			}
			d.Shuffle()
			r = Room{Updates: up, Timestamp: t, Slug: generateRoomName(3), Deck: d.GetSignature()}
			_, err = tx.Put(roomKey, &r)
			if err != nil {
				return fmt.Errorf("could not create updated room %v: %v", rk, err)
			}
		}
		if err != nil {
			return fmt.Errorf("issue updating room: %v", err)
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
		r.Modifier = modifier
		_, err = tx.Put(roomKey, &r)
		if err != nil {
			return fmt.Errorf("could not update room %v: %v", rk, err)
		}
		return nil
	})
	if err != nil {
		log.Printf("issue updating room: %v", err)
	}
}

func setBackground(c context.Context, rk, url string) {
	keyStr, err := getEncodedRoomKeyFromName(c, rk)
	if err != nil {
		log.Printf("roomname wonkiness in setBackground: %v", err)
		return
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		log.Printf("setBackground: could not decode room key %v: %v", rk, err)
		return
	}
	var r Room
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(roomKey, &r); err != nil {
			return fmt.Errorf("could not find room %v for setting background: %v", rk, err)
		}
		r.BgURL = url
		_, err = tx.Put(roomKey, &r)
		if err != nil {
			return fmt.Errorf("could not create updated room %v: %v", rk, err)
		}

		var testRoom Room
		if err = tx.Get(roomKey, &testRoom); err != nil {
			return fmt.Errorf("couldn't find the new entry: %v", err)
		}
		if testRoom.BgURL != url {
			return fmt.Errorf("url is wrong")
		}
		return nil
	})
	if err != nil {
		log.Printf("%v", err)
	}
	updateRoom(c, roomKey.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
}

func addCustomSet(c context.Context, rk, name, lines, height, width string) {
	keyStr, err := getEncodedRoomKeyFromName(c, rk)
	if err != nil {
		log.Printf("roomname wonkiness in addCustomSet: %v", err)
		return
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		log.Printf("addCustomSet: could not decode room key %v: %v", rk, err)
		return
	}
	var r Room
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(roomKey, &r); err != nil {
			return fmt.Errorf("could not find room %v for adding custom set: %v", rk, err)
		}
		cs, err := newCustomSetFromNewlineSeparatedString(lines, height, width)
		//cs, err := newCustomSet(url)
		if err != nil {
			return fmt.Errorf("issue with custom set: %v", err)
		}
		rcs, err := r.GetCustomSets()
		if err != nil {
			return fmt.Errorf("error in addCustomSet%v", err)
		}
		rcs[name] = cs
		err = r.SetCustomSets(rcs)
		if err != nil {
			return fmt.Errorf("other error in addCustomSet: %v", err)
		}
		_, err = tx.Put(roomKey, &r)
		if err != nil {
			return fmt.Errorf("could not create updated room %v: %v", rk, err)
		}

		var testRoom Room
		if err = tx.Get(roomKey, &testRoom); err != nil {
			return fmt.Errorf("couldn't find the new entry: %v", err)
		}
		return nil
	})
	updateRoom(c, roomKey.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
}

func removeCustomSet(c context.Context, rk, name string) {
	keyStr, err := getEncodedRoomKeyFromName(c, rk)
	if err != nil {
		log.Printf("roomname wonkiness in addCustomSet: %v", err)
		return
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		log.Printf("addCustomSet: could not decode room key %v: %v", rk, err)
		return
	}
	var r Room
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(roomKey, &r); err != nil {
			return fmt.Errorf("could not find room %v for removing custom set: %v", rk, err)
		}
		rcs, err := r.GetCustomSets()
		if err != nil {
			return fmt.Errorf("error in removeCustomSet%v", err)
		}
		delete(rcs, name)
		err = r.SetCustomSets(rcs)
		if err != nil {
			return fmt.Errorf("other error in removeCustomSet: %v", err)
		}
		_, err = tx.Put(roomKey, &r)
		if err != nil {
			return fmt.Errorf("could not create updated room %v: %v", rk, err)
		}

		var testRoom Room
		if err = tx.Get(roomKey, &testRoom); err != nil {
			return fmt.Errorf("couldn't find the new entry: %v", err)
		}
		return nil
	})
	updateRoom(c, roomKey.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
}

func refreshRoom(c context.Context, rk, fp string) string {
	roomKey, err := datastore.DecodeKey(rk)
	out := ""
	if err != nil {
		log.Printf("refreshRoom: could not decode room key %v: %v", rk, err)
		return out
	}
	var r Room
	var send []Update
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(roomKey, &r); err != nil {
			return fmt.Errorf("could not find room %v for refresh: %v", rk, err)
		}
		keep := []Update{}
		now := time.Now().Unix()
		var umUpdates []Update
		err = json.Unmarshal(r.Updates, &umUpdates)
		if err != nil {
			return fmt.Errorf("could not unmarshal updates in refreshRoom: %v", err)
		}
		for _, u := range umUpdates {
			q := now - u.Timestamp
			if q > refreshDelta {
				continue
			}
			keep = append(keep, u)
			if u.Updater != fp || u.UpdateAll {
				send = append(send, u)
			}
		}
		// Don't perform another insert if nothing has changed.
		if len(keep) == 0 {
			return nil
		}
		r.Updates, err = json.Marshal(keep)
		if err != nil {
			out = ""
			return fmt.Errorf("could not marshal updates in refreshRoom: %v", err)
		}
		_, err = tx.Put(roomKey, &r)
		if err != nil {
			return fmt.Errorf("could not create updated room %v: %v", rk, err)
		}
		return nil
	})
	if err != nil {
		log.Printf("%v", err)
		return out
	}
	if len(send) > 0 {
		toHash, err := json.Marshal(send)
		if err != nil {
			log.Printf("could not marshal updates to send in refreshRoom: %v", err)
			return ""
		}
		for _, u := range send {
			if u.Message != "" {
				out = fmt.Sprintf("%x||%s", md5.Sum(toHash), u.Message)
				break
			}
		}
		if out == "" {
			out = fmt.Sprintf("%x", md5.Sum(toHash))
		}
	}
	return out
}

// roomKey creates a new room entity key.
func roomKey() *datastore.Key {
	return datastore.IDKey("Room", time.Now().UnixNano(), nil)
}

// dieKey creates a new die entity key.
func dieKey(roomKey *datastore.Key, i int64) *datastore.Key {
	return datastore.IDKey("Die", time.Now().UnixNano()+i, roomKey)
}

func newRoom(c context.Context) (string, error) {
	up, err := json.Marshal([]Update{})
	if err != nil {
		return "", fmt.Errorf("could not marshal update: %v", err)
	}
	roomName := generateRoomName(3)
	deck.Seed()
	d, err := deck.New(deck.Unshuffled)
	if err != nil {
		log.Printf("could not create deck: %v", err)
	}
	d.Shuffle()
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		_, err = tx.Put(roomKey(), &Room{Updates: up, Timestamp: time.Now().Unix(), Slug: roomName, Deck: d.GetSignature()})
		if err != nil {
			return fmt.Errorf("could not create new room: %v", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return roomName, nil
}

func drawCards(c context.Context, count int, roomKey *datastore.Key, deckName, hidden, fp string) ([]*Die, []*datastore.Key) {
	dice := []*Die{}
	keys := []*datastore.Key{}
	var room Room
	_, err := dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err := tx.Get(roomKey, &room); err != nil {
			return fmt.Errorf("issue getting room in drawCards: %v", err)
		}
		ts := time.Now().Unix()
		if deckName == "" {
			hand, err := deck.New(deck.Empty)
			if err != nil {
				return fmt.Errorf("problem creating hand: %v", err)
			}
			deck.Seed()
			roomDeck, err := deck.New(deck.FromSignature(room.Deck))
			if err != nil {
				return fmt.Errorf("problem with deck signature: %v", err)
			}
			roomDeck.Shuffle()
			deckSize := roomDeck.NumberOfCards()
			// TODO(shanel): We *might* want to surface the need to shuffle the deck once there are no cards left.
			if deckSize == 0 || room.Deck == "" {
				log.Print("room deck is empty")
				// TODO(shanel): Figure out what it appears that the number of cards for an empty deck is 52
				// Below might be useless...
				var empty *deck.Deck
				empty, err = deck.New(deck.Empty)
				if err != nil {
					return fmt.Errorf("issue creating empty deck: %v", err)
				}
				room.Deck = empty.GetSignature()
				if _, err := tx.Put(roomKey, &room); err != nil {
					return fmt.Errorf("issue updating deck in drawCards: %v", err)
				}
			}
			if deckSize < count {
				roomDeck.Deal(deckSize, hand)
				log.Printf("not enough cards in room deck, only dealt %v", deckSize)
			} else {
				roomDeck.Deal(count, hand)
			}
			cards := strings.Split(strings.TrimSuffix(hand.String(), "\n"), "\n")
			for i, card := range cards {
				diu, err := getDieImageURL("card", card, "")
				if err != nil {
					log.Printf("could not get die image: %v", err)
				}
				dk := dieKey(roomKey, int64(i))
				d := Die{
					Size:      "card",
					Result:    0,
					ResultStr: card,
					Key:       dk,
					KeyStr:    dk.Encode(),
					Timestamp: ts,
					Image:     diu,
					New:       true,
					IsCard:    true,
				}
				if hidden != "" && hidden != "false" {
					d.HiddenBy = fp
					d.IsHidden = true
				}
				dice = append(dice, &d)
				keys = append(keys, dk)
			}
			room.Deck = roomDeck.GetSignature()
			if _, err := tx.Put(roomKey, &room); err != nil {
				return fmt.Errorf("issue updating room in drawCards: %v", err)
			}
		} else {
			// do the custom set stuff here...
			customSets, err := room.GetCustomSets()
			if err != nil {
				return fmt.Errorf("issue getting custom sets in drawCards: %v", err)
			}
			cs, ok := customSets[deckName]
			if !ok {
				return fmt.Errorf("no custom set with name %v", deckName)
			}
			drawn, err := cs.Draw(count)
			if err != nil {
				log.Printf("problem with custom draw: %v", err)
			}
			for i, card := range drawn {
				ii, err := strconv.Atoi(i)
				if err != nil {
					log.Printf("error in drawCards: %v", err)
					continue
				}
				diu := card
				dk := dieKey(roomKey, int64(ii))
				d := Die{
					Size:          "card", // should this be "custom" ???
					Result:        ii,
					ResultStr:     "",
					Key:           dk,
					KeyStr:        dk.Encode(),
					Timestamp:     ts,
					Image:         diu,
					New:           true,
					IsCustomItem:  true,
					IsCard:        true,
					CustomSetName: deckName,
					CustomHeight:  cs.MaxHeight,
					CustomWidth:   cs.MaxWidth,
				}
				if hidden != "" && hidden != "false" {
					d.HiddenBy = fp
					d.IsHidden = true
				}
				dice = append(dice, &d)
				keys = append(keys, dk)
			}
			customSets[deckName] = cs
			err = room.SetCustomSets(customSets)
			if err != nil {
				return fmt.Errorf("issue setting custom sets in drawCards: %v", err)
			}
			_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
				if _, err := tx.Put(roomKey, &room); err != nil {
					return fmt.Errorf("issue updating room in drawCards: %v", err)
				}
				var testRoom Room
				if err = tx.Get(roomKey, &testRoom); err != nil {
					return fmt.Errorf("couldn't find the new entry: %v", err)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		log.Printf("%v", err)
	}
	return dice, keys
}

var standardDice = map[string]bool{
	"3":   true,
	"4":   true,
	"5":   true,
	"6":   true,
	"6p":  true,
	"7":   true,
	"8":   true,
	"10":  true,
	"12":  true,
	"14":  true,
	"16":  true,
	"20":  true,
	"24":  true,
	"30":  true,
	"100": true,
	"F":   true,
	"H":   true,
}

func isFunky(d string) bool {
	_, ok := standardDice[d]
	return !ok && (d != "tokens")
}

func newRoll(c context.Context, sizes map[string]string, roomKey *datastore.Key, color, hidden, fp string) (int, error) {
	dice := []*Die{}
	keys := []*datastore.Key{}
	var totalCount int
	var total int
	ts := time.Now().Unix()
	unusual := map[string]bool{
		"label": true,
		"card":  true,
		"c4":    true,
		"c6":    true,
		"c8":    true,
		"ct":    true,
	}
	for size, v := range sizes {
		if _, ok := unusual[size]; !ok {
			if size == "xdy" {
				chunks := strings.Split(v, "d")
				if len(chunks) != 2 {
					continue
				}
				size = chunks[1]
				v = chunks[0]
			}
			var count int
			var err error
			count, err = strconv.Atoi(v)
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
					rs = "0"
				} else {
					r, rs = getNewResult(size)
				}
				if size != "F" && size != "H" {
					total += r
				}

				// SVG here
				var svg []byte
				var err error
				if !isFunky(size) {
					if size == "tokens" {
						svg, err = createSVG("token", rs, color)
					} else {
						svg, err = createSVG(fmt.Sprintf("d%s", size), rs, color)
					}
					if err != nil {
						log.Printf("svg creating issue: %v", err)
						continue
					}
				}

				var diu string
				dk := dieKey(roomKey, int64(i))
				d := Die{
					Size:      size,
					Result:    r,
					ResultStr: rs,
					Key:       dk,
					KeyStr:    dk.Encode(),
					Timestamp: ts,
					Image:     diu,
					New:       true,
					Color:     color,
					SVGBytes:  svg,
				}
				if color == "clear" {
					d.Color = "lightblue"
				}
				if isFunky(size) {
					d.ResultStr = fmt.Sprintf("%s (d%s)", d.ResultStr, size)
					d.IsLabel = true
					d.IsFunky = true
				} else {
					svgPath, err := getSVGPath(rs, size)
					if err != nil {
						log.Printf("could not get SVGPath: %v", err)
						continue
					}
					d.SVGPath = svgPath
					d.Version = 1
				}
				dice = append(dice, &d)
				keys = append(keys, dk)
			}
		}
	}

	// Do clocks
	clocks := []string{
		"c4",
		"c6",
		"c8",
		"ct",
	}
	for _, size := range clocks {
		if sizes[size] != "" {
			var p string
			lk := dieKey(roomKey, int64(len(dice)))
			d := fmt.Sprintf("clocks/%s-0.png", size)
			if u, ok := diceURLs[d]; ok {
				p = u
			} else {
				p = fmt.Sprintf("https://storage.googleapis.com/%v/die_images/%s", bucket, d)
				diceURLs[d] = p
			}
			l := Die{
				Size:      size,
				Result:    0,
				ResultStr: sizes[size],
				Image:     p,
				Key:       lk,
				KeyStr:    lk.Encode(),
				Timestamp: ts,
				New:       true,
				IsClock:   true,
			}
			dice = append(dice, &l)
			keys = append(keys, lk)
		}
	}

	if sizes["label"] != "" {
		lk := dieKey(roomKey, int64(len(dice)))
		l := Die{
			ResultStr: sizes["label"],
			Key:       lk,
			KeyStr:    lk.Encode(),
			Timestamp: ts,
			New:       true,
			IsLabel:   true,
		}
		dice = append(dice, &l)
		keys = append(keys, lk)
	}
	// TODO(shanel): Need to integrate custom card stuff here
	if sizes["card"] != "" {
		count, err := strconv.Atoi(sizes["card"])
		if err == nil {
			cards, cardKeys := drawCards(c, count, roomKey, "", hidden, fp)
			for _, card := range cards {
				dice = append(dice, card)
			}
			for _, ck := range cardKeys {
				keys = append(keys, ck)
			}
		}
	}
	keyStrings := []string{}
	for _, k := range keys {
		keyStrings = append(keyStrings, k.Encode())
	}
	_, err := dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		_, err := tx.PutMulti(keys, dice)
		if err != nil {
			return fmt.Errorf("could not create new dice: %v", err)
		}
		return nil
	})
	return total, err
}

func getRoomCards(c context.Context, encodedRoomKey string) ([]Die, error) {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return nil, fmt.Errorf("getRoomCards: could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).Filter("Size =", "card") //.Limit(10)
	dice := []Die{}
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if _, err = dsClient.GetAll(c, q, &dice); err != nil {
			return fmt.Errorf("problem executing card query: %v", err)
		}
		return nil
	})
	return dice, err
}

func getRoomCustomCards(c context.Context, encodedRoomKey string) ([]Die, error) {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return nil, fmt.Errorf("getRoomCustomCards: could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).Filter("IsCustomItem =", true) //.Limit(10)
	dice := []Die{}
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if _, err = dsClient.GetAll(c, q, &dice); err != nil {
			return fmt.Errorf("problem executing custom card query: %v", err)
		}
		return nil
	})
	return dice, err
}

func getRoomDice(c context.Context, encodedRoomKey, order, sort string) ([]Die, error) {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return nil, fmt.Errorf("getRoomDice: could not decode room key %v: %v", encodedRoomKey, err)
	}
	var q *datastore.Query
	var bSort bool
	bSort, err = strconv.ParseBool(sort)
	if err != nil {
		bSort = true
	}
	if bSort {
		q = datastore.NewQuery("Die").Ancestor(k).Order(order) //.Limit(10)
	} else {
		q = datastore.NewQuery("Die").Ancestor(k)
	}
	dice := []Die{}
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if _, err = dsClient.GetAll(c, q, &dice); err != nil {
			return fmt.Errorf("problem executing dice query: %v", err)
		}
		return nil
	})
	for _, d := range dice {
		d.SVG = template.HTML(fmt.Sprintf("%s", d.SVGBytes))
	}
	return dice, err
}

// TODO(shanel): If more than 500 things are altered RPC will fail. Need to batch in that case.
func clearRoomDice(c context.Context, encodedRoomKey string) error {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return fmt.Errorf("clearRoomDice: could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).KeysOnly()
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		out := dsClient.Run(c, q)
		nuke := []*datastore.Key{}
		for {
			d, err := out.Next(nil)
			if err != nil {
				break
			}
			nuke = append(nuke, d)
		}
		err = tx.DeleteMulti(nuke)
		if err != nil {
			return fmt.Errorf("problem deleting room dice from room %v: %v", encodedRoomKey, err)
		}
		return nil
	})
	// Fake updater so Safari will work?
	updateRoom(c, k.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	return err
}

func getDieImageURL(size, result, color string) (string, error) {
	// Fate dice silliness
	ft := map[string]string{"-": "minus", "+": "plus", " ": "zero"}
	if _, ok := ft[result]; ok {
		result = ft[result]
	}
	var d string
	if size == "card" {
		d = cardToPNG[result]
	} else {
		d = fmt.Sprintf("%s-d%s/%s.png", color, size, result)
	}
	if size == "0" || result == "token" {
		d = fmt.Sprintf("tokens/%s_token.png", color)
	}
	// Should this have a mutex?
	if u, ok := diceURLs[d]; ok {
		return u, nil
	}
	var p string
	if size == "card" {
		p = fmt.Sprintf("https://storage.googleapis.com/%v/playing_cards/%s", bucket, d)
	} else {
		p = fmt.Sprintf("https://storage.googleapis.com/%v/die_images/%s", bucket, d)
	}
	diceURLs[d] = p
	return p, nil
}

func getSVGPath(result, size string) (string, error) {
	// Fate dice silliness
	d := fmt.Sprintf("d%s.svg", size)
	if size == "0" || result == "token" {
		d = "token.svg"
	}
	// Should this have a mutex?
	if u, ok := diceURLs[d]; ok {
		return u, nil
	}
	p := fmt.Sprintf("/js/%s", d)
	diceURLs[d] = p
	return p, nil
}

func updateDieLocation(c context.Context, encodedDieKey, fp string, x, y float64) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(k, &d); err != nil {
			return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
		}
		d.updatePosition(x, y)
		_, err = tx.Put(k, &d)
		if err != nil {
			return fmt.Errorf("could not update die %v with new position: %v", encodedDieKey, err)
		}
		return nil
	})
	updateRoom(c, k.Parent.Encode(), Update{Updater: fp, Timestamp: time.Now().Unix()}, 0)
	return err
}

func deleteDieHelper(c context.Context, encodedDieKey string) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(k, &d); err != nil {
			return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
		}
		err = tx.Delete(k)
		if err != nil {
			return fmt.Errorf("problem deleting room die %v: %v", encodedDieKey, err)
		}
		return nil
	})
	// Fake updater so Safari will work?
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	return err
}

func fateReplace(in string) string {
	ft := map[string]string{"-": "1", "+": "3", " ": "2"}
	if r, ok := ft[in]; ok {
		return r
	}
	return in
}

// TODO(shanel): This will need to handle new cards
func revealDieHelper(c context.Context, encodedDieKey, fp string) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(k, &d); err != nil {
			return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
		}
		if d.HiddenBy != fp && d.HiddenBy != "" {
			return fmt.Errorf("item with key %v was not hidden by %v", encodedDieKey, fp)
		}
		if d.IsCard || d.IsCustomItem || d.IsImage || d.IsClock || d.Size == "tokens" {
			d.IsHidden = false
			d.HiddenBy = ""
			_, err = tx.Put(k, &d)
			if err != nil {
				return fmt.Errorf("problem revealing room die %v: %v", encodedDieKey, err)
			}
			// Fake updater so Safari will work?
			updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
			return nil
		}
		return fmt.Errorf("only cards and custom items can be revealed.")
	})
	return err
}

func hideDieHelper(c context.Context, encodedDieKey, hiddenBy string) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(k, &d); err != nil {
			return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
		}
		if d.IsHidden && d.HiddenBy != "" {
			return fmt.Errorf("item is already hidden")
		}
		if d.IsCard || d.IsCustomItem || d.IsClock || d.IsImage || d.Size == "tokens" {
			d.IsHidden = true
			d.HiddenBy = hiddenBy
			_, err = tx.Put(k, &d)
			if err != nil {
				return fmt.Errorf("problem hiding room die %v: %v", encodedDieKey, err)
			}
			// Fake updater so Safari will work?
			updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
			return nil
		}
		return fmt.Errorf("Only cards and custom items can be hidden.")
	})
	return err
}

func getOldColor(u string) string {
	chunk := strings.Split(u, "/")[5]
	var c string
	if chunk == "tokens" {
		c = strings.Split(strings.Split(u, "/")[6], "_")[0]
		if c == "clear" {
			return "lightblue"
		}
		return c
	} else {
		c = strings.Split(chunk, "-")[0]
		if c == "clear" {
			return "lightblue"
		}
		return c
	}
}

func rerollDieHelper(c context.Context, encodedDieKey, room, fp string, white bool) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(k, &d); err != nil {
			return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
		}
		if d.IsHidden && d.HiddenBy != fp {
			return fmt.Errorf("wont reroll die with key %v - not hidden by %v", encodedDieKey, fp)
		}
		if (d.IsLabel || d.IsImage) && !d.IsFunky {
			return fmt.Errorf("label")
		}

		if d.IsFunky {
			d.Result, d.ResultStr = getNewResult(d.Size)
			d.ResultStr = fmt.Sprintf("%s (d%s)", d.ResultStr, d.Size)
			d.Timestamp = time.Now().Unix()
		} else if d.IsCustomItem {
			// Do a single draw.
			dice, keys := drawCards(c, 1, k.Parent, d.CustomSetName, strconv.FormatBool(d.IsHidden), d.HiddenBy)
			// Set the location to the same as the passed in die.
			d.ResultStr = dice[0].ResultStr
			d.Image = dice[0].Image
			// Delete the old die.
			err := deleteDieHelper(c, keys[0].Encode())
			if err != nil {
				log.Printf("error in deleteDieHelper: %v", err)
			}
		} else if d.IsCard {
			dice, keys := drawCards(c, 1, k.Parent, "", strconv.FormatBool(d.IsHidden), d.HiddenBy)
			// Set the location to the same as the passed in die.
			d.ResultStr = dice[0].ResultStr
			d.Image = dice[0].Image
			// Delete the old die.
			err := deleteDieHelper(c, keys[0].Encode())
			if err != nil {
				log.Printf("error in deleteDieHelper: %v", err)
			}
		} else if d.IsClock {
			sep := map[string]int{
				"c4": 5,
				"c6": 7,
				"c8": 9,
				"ct": 7,
			}
			oldResult := d.Result
			d.Result = (d.Result + 1) % sep[d.Size]
			d.Image = strings.Replace(d.Image, fmt.Sprintf("%d.png", oldResult), fmt.Sprintf("%d.png", d.Result), 1)
		} else {
			if d.SVGPath == "" {
				svgPath, err := getSVGPath(d.ResultStr, d.Size)
				if err != nil {
					log.Printf("could not get SVGPath: %v", err)
				} else {
					d.SVGPath = svgPath
					d.Version = 1
				}
			}
			oldResultStr := fateReplace(d.ResultStr)
			if d.Color == "" {
				d.Color = getOldColor(d.Image)
			}
			if d.Size == "tokens" {
				if d.Result == 0 {
					d.Result = 1
					d.ResultStr = "1"
				} else {
					d.Result = 0
					d.ResultStr = "0"
				}
				if white {
					d.Result = 0
					d.ResultStr = "0"
					if d.OldColor == "" {
						d.OldColor = d.Color
						d.Color = "white"
					} else {
						d.Color = d.OldColor
						d.OldColor = ""
					}
				}
			} else {
				d.Result, d.ResultStr = getNewResult(d.Size)
				log.Printf("result: %v; resultstr: %v", d.Result, d.ResultStr)
			}
			// SVG here
			var svg []byte
			var err error
			if !isFunky(d.Size) {
				if d.Size == "tokens" {
					svg, err = createSVG("token", d.ResultStr, d.Color)
				} else {
					svg, err = createSVG(fmt.Sprintf("d%s", d.Size), d.ResultStr, d.Color)
				}
				if err != nil {
					log.Printf("svg creating issue: %v", err)
				} else {
					d.SVGBytes = svg
				}
			}
			d.Timestamp = time.Now().Unix()
			if d.Image != "" {
				d.Image = strings.Replace(d.Image, fmt.Sprintf("%s.png", oldResultStr), fmt.Sprintf("%s.png", fateReplace(d.ResultStr)), 1)
			}
		}
		_, err = tx.Put(k, &d)
		if err != nil {
			return fmt.Errorf("problem rerolling room die %v: %v", encodedDieKey, err)
		}
		if lastRoll[room] == 0 || lastAction[room] == "reroll" {
			if d.Size != "F" && d.Size != "H" && !d.IsCard {
				lastRoll[room] += d.Result
			}
		}
		return nil
	})
	// Fake updater so Safari will work?
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	return err
}

func decrementClock(c context.Context, encodedDieKey string) error {
	k, err := datastore.DecodeKey(encodedDieKey)
	if err != nil {
		return fmt.Errorf("could not decode die key %v: %v", encodedDieKey, err)
	}
	var d Die
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if err = tx.Get(k, &d); err != nil {
			return fmt.Errorf("could not find die with key %v: %v", encodedDieKey, err)
		}
		sep := map[string]int{
			"c4": 5,
			"c6": 7,
			"c8": 9,
			"ct": 7,
		}
		if d.Result == 0 { // No need to wrap around.
			return nil
		}
		oldResult := d.Result
		d.Result = (d.Result - 1) % sep[d.Size]
		d.Image = strings.Replace(d.Image, fmt.Sprintf("%d.png", oldResult), fmt.Sprintf("%d.png", d.Result), 1)
		_, err = tx.Put(k, &d)
		if err != nil {
			return fmt.Errorf("problem decrementing clock %v: %v", encodedDieKey, err)
		}
		return nil
	})
	// Fake updater so Safari will work?
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	updateRoom(c, k.Parent.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	return err
}

func getNewResult(kind string) (int, string) {
	var s int
	var err error
	if kind == "10p" { // TODO(shanel): this can probably go away due to d100
		s = 10
	} else if kind == "6p" {
		s = 6
	} else {
		s, err = strconv.Atoi(kind)
		if err != nil {
			if kind == "F" {
				r := rand.Intn(3)
				return r + 1, fmt.Sprintf("%d", r+1)
			} else {
				r := rand.Intn(2)
				return r, fmt.Sprintf("%d", r)
			}
		}
	}
	r := rand.Intn(s) + 1
	return r, strconv.Itoa(r)
}

func main() {
	//	func init() {
	http.HandleFunc("/", Root)
	http.HandleFunc("/about", About)
	http.HandleFunc("/addcustomset", HandleAddingCustomSet)
	http.HandleFunc("/alert", Alert)
	http.HandleFunc("/background", Background)
	http.HandleFunc("/clear", Clear)
	http.HandleFunc("/delete", DeleteDie)
	http.HandleFunc("/decrementclock", HandleDecrementClock)
	http.HandleFunc("/draw", Draw)
	http.HandleFunc("/hide", HideDie)
	http.HandleFunc("/image", AddImage)
	http.HandleFunc("/move", Move)
	http.HandleFunc("/paused", Paused)
	http.HandleFunc("/refresh", Refresh)
	http.HandleFunc("/removecustomset", HandleRemovingCustomSet)
	http.HandleFunc("/reroll", RerollDie)
	http.HandleFunc("/reveal", RevealDie)
	http.HandleFunc("/roll", Roll)
	http.HandleFunc("/room", GetRoom)
	http.HandleFunc("/room/", GetRoom)
	http.HandleFunc("/room/*", GetRoom)
	http.HandleFunc("/safety", SafetyRoom)
	http.HandleFunc("/safety/", SafetyRoom)
	http.HandleFunc("/safety/*", SafetyRoom)
	http.HandleFunc("/shuffle", Shuffle)

	// Seed random number generator.
	rand.Seed(int64(time.Now().Unix()))

	ctx := context.Background()
	var err error
	dsClient, err = datastore.NewClient(ctx, "just-another-dice-roller")
	if err != nil {
		log.Fatal(err)
	}

	// [START setting_port]
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
	// [END setting_port]

}

func Root(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err == nil {
		smartRedirect(w, r, fmt.Sprintf("/room/%v", roomCookie.Value), http.StatusFound)
	}
	// If no cookie, then create a room, set cookie, and redirect
	room, err := newRoom(c)
	if err != nil {
		// TODO(shanel): This should probably say something more...
		log.Printf("no room from root: %v", err)
		http.NotFound(w, r)
	}
	http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room, SameSite: http.SameSiteLaxMode})
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func Paused(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	out := "<html><center>To save on bandwidth we have stopped updating you since you have been idle for half an hour. To get back to your room, click <a href=\"/room/%v\">here</a>.</center></html>"
	room := r.Form.Get("id")
	lastAction[room] = "paused"
	_, _ = fmt.Fprintf(w, out, room)
}

func Refresh(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	if _, ok := repeatOffenders[r.Form.Get("id")]; ok {
		http.NotFound(w, r)
		return
	}
	keyStr, err := getEncodedRoomKeyFromName(c, r.Form.Get("id"))
	if err != nil {
		log.Printf("roomname wonkiness in refresh: %v", err)
	}
	fp := r.Form.Get("fp")
	ref := refreshRoom(c, keyStr, fp)
	_, _ = fmt.Fprintf(w, "%v", ref)
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

func Move(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	keyStr := r.Form.Get("id")
	fp := r.Form.Get("fp")
	x, y := getXY(keyStr, r)
	err := updateDieLocation(c, keyStr, fp, x, y)
	if err != nil {
		log.Printf("quietly not updating position of %v to (%v, %v): %v", keyStr, x, y, err)
	}
	room := path.Base(r.Referer())
	lastAction[room] = "move"
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func Background(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	bg := r.Form.Get("bg")
	c := r.Context()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in background: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	setBackground(c, room, bg)
	updateRoom(c, roomKey.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func HandleAddingCustomSet(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	name := r.Form.Get("name")
	entries := r.Form.Get("entries")
	height := r.Form.Get("height")
	width := r.Form.Get("width")
	c := r.Context()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in handleAddingCustonSet: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	addCustomSet(c, room, name, entries, height, width)
	updateRoom(c, roomKey.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func HandleRemovingCustomSet(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	name := r.Form.Get("deck")
	c := r.Context()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in handleAddingCustomSet: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	removeCustomSet(c, room, name)
	updateRoom(c, roomKey.Encode(), Update{Updater: "safari y u no work", Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func Alert(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	message := r.Form.Get("message")
	c := r.Context()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in alert: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	updateRoom(c, roomKey.Encode(), Update{Updater: "", Timestamp: time.Now().Unix(), Message: message}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func AddImage(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in roll: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	_ = r.ParseForm()
	ts := time.Now().Unix()
	lk := dieKey(roomKey, int64(ts))
	l := Die{
		ResultStr:    "image",
		Key:          lk,
		KeyStr:       lk.Encode(),
		Timestamp:    ts,
		New:          true,
		IsImage:      true,
		Image:        r.Form.Get("url"),
		CustomHeight: r.Form.Get("height"),
		CustomWidth:  r.Form.Get("width"),
	}
	_, err = dsClient.Put(c, lk, &l)
	if err != nil {
		log.Printf("could not create new image: %v", err)
	}
	fp := r.Form.Get("fp")
	lastAction[room] = "image"
	updateRoom(c, keyStr, Update{Updater: fp, Timestamp: time.Now().Unix()}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func Roll(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
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
		"3":      r.FormValue("d3"),
		"4":      r.FormValue("d4"),
		"5":      r.FormValue("d5"),
		"6":      r.FormValue("d6"),
		"6p":     r.FormValue("d6p"),
		"7":      r.FormValue("d7"),
		"8":      r.FormValue("d8"),
		"10":     r.FormValue("d10"),
		"12":     r.FormValue("d12"),
		"14":     r.FormValue("d14"),
		"16":     r.FormValue("d16"),
		"20":     r.FormValue("d20"),
		"24":     r.FormValue("d24"),
		"30":     r.FormValue("d30"),
		"100":    r.FormValue("d100"),
		"F":      r.FormValue("dF"),
		"H":      r.FormValue("dH"),
		"label":  r.FormValue("label"),
		"card":   r.FormValue("cards"),
		"tokens": r.FormValue("tokens"),
		"xdy":    r.FormValue("xdy"),
		"c4":     r.FormValue("c4"),
		"c6":     r.FormValue("c6"),
		"c8":     r.FormValue("c8"),
		"ct":     r.FormValue("ct"),
	}
	fp := r.FormValue("fp")
	col := r.FormValue("color")
	mod := r.FormValue("modifier")
	mod = strings.TrimLeft(mod, " +")
	var modInt int
	modInt, err = strconv.Atoi(mod)
	if err != nil {
		modInt = 0
	}
	total, err := newRoll(c, toRoll, roomKey, col, r.FormValue("hiddenDraw"), fp)
	if err != nil {
		log.Printf("error in roll: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	lastRoll[room] = total

	lastAction[room] = "roll"
	updateRoom(c, roomKey.Encode(), Update{Updater: fp, Timestamp: time.Now().Unix()}, modInt)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func DeleteDie(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	keyStr := r.Form.Get("id")
	room := path.Base(r.Referer())
	// Do we need to be worried dice will be deleted from other rooms?
	err := deleteDieHelper(c, keyStr)
	if err != nil {
		log.Printf("error in deleteDie: %v", err)
		smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	lastAction[room] = "delete"
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func RevealDie(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	keyStr := r.Form.Get("id")
	fp := r.Form.Get("fp")
	room := path.Base(r.Referer())
	lastRoll[room] = 0
	// Do we need to be worried dice will be revealed from other rooms?
	err := revealDieHelper(c, keyStr, fp)
	if err != nil {
		log.Printf("error in revealDie: %v", err)
		smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	lastAction[room] = "reveal"
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func HideDie(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	keyStr := r.Form.Get("id")
	room := path.Base(r.Referer())
	lastRoll[room] = 0
	// Do we need to be worried dice will be revealed from other rooms?
	err := hideDieHelper(c, keyStr, r.Form.Get("fp"))
	if err != nil {
		log.Printf("error in hideDie: %v", err)
		smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	lastAction[room] = "hide"
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func RerollDie(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	keyStr := r.Form.Get("id")
	fp := r.Form.Get("fp")
	var white bool
	var err error
	white, err = strconv.ParseBool(r.Form.Get("white"))
	if err != nil {
		log.Printf("issue with flipping token: %v", err)
	}
	room := path.Base(r.Referer())
	lastRoll[room] = 0
	// Do we need to be worried dice will be rerolled from other rooms?
	err = rerollDieHelper(c, keyStr, room, fp, white)
	if err != nil {
		log.Printf("error in rerollDie: %v", err)
		smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	lastAction[room] = "reroll"
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func HandleDecrementClock(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	keyStr := r.Form.Get("id")
	room := path.Base(r.Referer())
	err := decrementClock(c, keyStr)
	if err != nil {
		log.Printf("error in decrementClock: %v", err)
		smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
	}
	lastAction[room] = "decrementClock"
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func Clear(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in clear: %v", err)
	}
	err = clearRoomDice(c, keyStr)
	if err != nil {
		log.Printf("clear failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fp := r.Form.Get("fp")
	lastAction[room] = "clear"
	updateRoom(c, keyStr, Update{Updater: fp, Timestamp: time.Now().Unix()}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func GetRoom(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	room := path.Base(r.URL.Path)
	if _, ok := repeatOffenders[room]; ok {
		http.NotFound(w, r)
		return
	}
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		repeatOffenders[room] = true
		log.Printf("room wonkiness in room: %v", err)
	}
	sort := "true"
	if cook, err := r.Cookie("sort_dice"); err == nil {
		sort = cook.Value
	}
	dice, err := getRoomDice(c, keyStr, "Result", sort)
	if err != nil {
		newRoom, err := newRoom(c)
		if err != nil {
			log.Printf("no room because: %v", err)
			// TODO(shanel): This should probably say something more...
			http.NotFound(w, r)
		}
		http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: newRoom, SameSite: http.SameSiteLaxMode})
		time.Sleep(100 * time.Nanosecond) // Getting into a race I think...
		repeatOffenders[room] = true
		smartRedirect(w, r, fmt.Sprintf("/room/%v", newRoom), http.StatusFound)
		return
	}

	diceForTotals, err := getRoomDice(c, keyStr, "-Timestamp", "true")
	if err != nil {
		log.Printf("could not get dice for totals: %v", err)
	}
	var (
		rollTotal       int
		rollCount       int
		rollAvg         float64
		roomTotal       int
		roomCount       int
		roomAvg         float64
		newestTimestamp int64
		tokenCount      int
	)
	for i, d := range diceForTotals {
		if i == 0 {
			newestTimestamp = d.Timestamp
		}
		realSize := d.Size
		if realSize == "6p" {
			realSize = "6"
		}
		if _, err := strconv.Atoi(realSize); err == nil {
			roomTotal += d.Result
			roomCount++
			if newestTimestamp == d.Timestamp {
				rollTotal += d.Result
				rollCount++
			}
		}
		if d.Size == "tokens" {
			tokenCount++
		}
	}

	rollAvg = float64(rollTotal) / float64(rollCount)
	roomAvg = float64(roomTotal) / float64(roomCount)

	cookie := &http.Cookie{Name: "dice_room", Value: room, SameSite: http.SameSiteLaxMode}
	http.SetCookie(w, cookie)

	var rm Room
	var deckSize int
	k, err := datastore.DecodeKey(keyStr)
	if err != nil {
		log.Printf("room: could not decode room key %v: %v", keyStr, err)
	} else {
		err := dsClient.Get(c, k, &rm)
		if err != nil {
			log.Printf("could not find room: %v", err)
		} else {
			roomDeck, err := deck.New(deck.FromSignature(rm.Deck))
			if err != nil {
				log.Printf("problem with deck signature: %v", err)
			} else {
				deckSize = roomDeck.NumberOfCards()
			}
		}
	}
	// Cull out cards that should not be seen...
	filteredDice := []Die{}
	fp := ""
	if cook, err := r.Cookie("fp"); err == nil {
		fp = cook.Value
	}
	for _, tf := range dice {
		if tf.HiddenBy == fp || !tf.IsHidden {
			filteredDice = append(filteredDice, tf)
			continue
		}
		if tf.IsHidden && tf.IsCard {
			tf.Image = fmt.Sprintf("https://storage.googleapis.com/%v/playing_cards/back.png", bucket)
			tf.IsHidden = false
			filteredDice = append(filteredDice, tf)
		}
	}
	p := Passer{
		Dice:              filteredDice,
		RoomTotal:         roomTotal,
		RoomAvg:           roomAvg,
		RollTotal:         rollTotal,
		RollAvg:           rollAvg,
		CardsLeft:         deckSize,
		CustomSets:        []PassedCustomSet{},
		Modifier:          rm.Modifier,
		ModifiedRollTotal: rollTotal + rm.Modifier,
		TokenCount:        tokenCount,
	}
	var latestUpdate int64
	for _, v := range p.Dice {
		if v.Timestamp > latestUpdate {
			latestUpdate = v.Timestamp
		}
	}
	p.LastChangeTimestamp = time.Unix(latestUpdate, 0).Format("15:04:05")
	rcs, err := rm.GetCustomSets()
	if err != nil {
		log.Printf("problem with custom sets: %v", err)
	} else {
		for i, s := range rcs {
			sn := strings.Replace(i, " ", "_", -1)
			pcs := PassedCustomSet{len(s.Instance), i, sn, template.JS(fmt.Sprintf("pull_from_%s()", sn)), template.JS(fmt.Sprintf("randomize_discards_from_%s()", sn)), template.JS(fmt.Sprintf("remove_%s()", sn)), template.JS(s.MaxHeight), template.JS(s.MaxWidth)}
			p.CustomSets = append(p.CustomSets, pcs)
		}
	}
	if rm.BgURL != "" {
		p.BgURL = rm.BgURL
		p.HasBgURL = true
	}
	if la, ok := lastAction[room]; ok {
		if la == "delete" {
			var lr int
			if lr, ok = lastRoll[room]; ok {
				p.RollTotal = lr
			}
		}
	}
	content, err := ioutil.ReadFile("room.tmpl.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	roomTemplate := template.Must(template.New("room").Funcs(template.FuncMap{
		"noescape": noescape,
		"hidden":   hidden,
	}).Parse(string(content[:])))
	if err := roomTemplate.Execute(w, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func SafetyRoom(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	room := path.Base(r.URL.Path)
	if _, ok := repeatOffenders[room]; ok {
		http.NotFound(w, r)
		return
	}
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		repeatOffenders[room] = true
		log.Printf("room wonkiness in room: %v", err)
	}
	_, err = getRoomDice(c, keyStr, "Result", "true")
	if err != nil {
		newRoom, err := newRoom(c)
		if err != nil {
			log.Printf("no room because: %v", err)
			// TODO(shanel): This should probably say something more...
			http.NotFound(w, r)
		}
		http.SetCookie(w, &http.Cookie{Name: "safety_room", Value: newRoom, SameSite: http.SameSiteLaxMode})
		time.Sleep(100 * time.Nanosecond) // Getting into a race I think...
		repeatOffenders[room] = true
		smartRedirect(w, r, fmt.Sprintf("/safety/%v", newRoom), http.StatusFound)
		return
	}

	cookie := &http.Cookie{Name: "dice_room", Value: room, SameSite: http.SameSiteLaxMode}
	http.SetCookie(w, cookie)

	var rm Room
	k, err := datastore.DecodeKey(keyStr)
	if err != nil {
		log.Printf("room: could not decode room key %v: %v", keyStr, err)
	} else {
		err := dsClient.Get(c, k, &rm)
		if err != nil {
			log.Printf("could not find room: %v", err)
		}
	}
	content, err := ioutil.ReadFile("safety.tmpl.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	roomTemplate := template.Must(template.New("safety").Funcs(template.FuncMap{
		"noescape": noescape,
		"hidden":   hidden,
	}).Parse(string(content[:])))
	if err := roomTemplate.Execute(w, Passer{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func noescape(b []byte) template.HTML {
	return template.HTML(fmt.Sprintf("%s", b))
}

func hidden(h bool) string {
	if h {
		return "hidden "
	}
	return ""
}

func About(w http.ResponseWriter, _ *http.Request) {
	if out, err := ioutil.ReadFile("about.html"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		_, _ = fmt.Fprintf(w, "%s", out)
	}
}

func shuffleDiscards(c context.Context, keyStr, deckName string) error {
	_, err := dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		if deckName != "" {
			cards, err := getRoomCustomCards(c, keyStr)
			if err != nil {
				return err
			}
			stillOut := map[string]bool{}
			for _, k := range cards {
				stillOut[strconv.Itoa(k.Result)] = true
			}
			roomKey, err := datastore.DecodeKey(keyStr)
			if err != nil {
				return fmt.Errorf("shuffleDeck: could not decode room key %v: %v", keyStr, err)
			}
			var r Room
			t := time.Now().Unix()
			if err = tx.Get(roomKey, &r); err != nil {
				return err
			}
			cs, err := r.GetCustomSets()
			if err != nil {
				return err
			}
			toShuffle, ok := cs[deckName]
			if !ok {
				return fmt.Errorf("could not find custom set %v", deckName)
			}
			toShuffle.shuffleDiscards(stillOut)
			cs[deckName] = toShuffle
			err = r.SetCustomSets(cs)
			if err != nil {
				return fmt.Errorf("issue in SetCustomSets: %v", err)
			}
			r.Timestamp = t
			_, err = tx.Put(roomKey, &r)
			if err != nil {
				return fmt.Errorf("could not create updated room %v: %v", keyStr, err)
			}
			if err = tx.Get(roomKey, &r); err != nil {
				return err
			}
		} else {
			cards, err := getRoomCards(c, keyStr)
			if err != nil {
				return err
			}
			roomCardStrings := map[string]bool{}
			for _, card := range cards {
				roomCardStrings[card.ResultStr] = true
			}
			sig := ""
			withCards := []deck.Card{}
			for k := range cardToPNG {
				if _, ok := roomCardStrings[k]; !ok {
					pieces := strings.Split(k, "")
					cc := deck.Card(faceMap[pieces[0]]*4 + suitMap[pieces[1]])
					withCards = append(withCards, cc)
				}
			}
			deck.Seed()
			d, err := deck.New(deck.WithCards(withCards...))
			if err != nil {
				return err
			}
			d.Shuffle()
			sig = d.GetSignature()
			roomKey, err := datastore.DecodeKey(keyStr)
			if err != nil {
				return fmt.Errorf("shuffleDeck: could not decode room key %v: %v", keyStr, err)
			}
			var r Room
			t := time.Now().Unix()
			if err = tx.Get(roomKey, &r); err != nil {
				return err
			}
			r.Deck = sig
			r.Timestamp = t
			_, err = tx.Put(roomKey, &r)
			if err != nil {
				return fmt.Errorf("could not create updated room %v: %v", keyStr, err)
			}
			if err = tx.Get(roomKey, &r); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func Shuffle(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	_ = r.ParseForm()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in shuffle: %v", err)
	}
	err = shuffleDiscards(c, keyStr, r.Form.Get("deck"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fp := r.Form.Get("fp")
	lastAction[room] = "shuffle"
	updateRoom(c, keyStr, Update{Updater: fp, Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}

func Draw(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	c := r.Context()
	room := path.Base(r.Referer())
	keyStr, err := getEncodedRoomKeyFromName(c, room)
	if err != nil {
		log.Printf("roomname wonkiness in draw: %v", err)
	}
	roomKey, err := datastore.DecodeKey(keyStr)
	if err != nil {
		log.Printf("draw: could not decode room key %v: %v", keyStr, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	count, err := strconv.Atoi(r.Form.Get("count"))
	if err != nil {
		if r.Form.Get("count") == "" {
			count = 1
		} else {
			log.Printf("error in draw: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
	fp := r.Form.Get("fp")
	dice, keys := drawCards(c, count, roomKey, r.Form.Get("deck"), r.Form.Get("hidden"), fp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	_, err = dsClient.RunInTransaction(c, func(tx *datastore.Transaction) error {
		_, err = tx.PutMulti(keys, dice)
		if err != nil {
			return fmt.Errorf("could not create new dice: %v", err)
		}
		if err = tx.Get(roomKey, &Room{}); err != nil {
			return fmt.Errorf("other error in draw: %v", err)
		}
		return nil
	})
	if err != nil {
		log.Printf("%v", err)
	}
	lastAction[room] = "draw"
	updateRoom(c, keyStr, Update{Updater: fp, Timestamp: time.Now().Unix(), UpdateAll: true}, 0)
	smartRedirect(w, r, fmt.Sprintf("/room/%v", room), http.StatusFound)
}
