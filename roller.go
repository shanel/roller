package roller

import (
	"fmt"
	"golang.org/x/net/context"
	"html/template"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"appengine"
	"appengine/blobstore"
	"appengine/datastore"
	"appengine/image"
	"google.golang.org/appengine/log"
	// Maybe use this later?
	//"appengine/user"
)

func init() {
	http.HandleFunc("/", root)
	http.HandleFunc("/room", room)
	http.HandleFunc("/roll", roll)
	http.HandleFunc("/clear", clear)
}

// As we create urls for the die images, store them here so we don't keep making them
var diceURLs = map[string]*url.URL{}

type Room struct{}

type Die struct {
	sync.RWMutex
	Size      string // for fate dice this won't be an integer
	Result    int    // For fate dice make this one of three very large numbers?
	ResultStr string
	Color     string
	Label     string
	x         int
	y         int
	Key       *datastore.Key
	Timestamp time.Time
	Image     string
}

func (d *Die) updatePosition(x, y int) {
	d.Lock()
	defer d.Unlock()
	d.x = x
	d.y = y
}

func (d *Die) getPosition() (int, int) {
	d.RLock()
	defer d.RUnlock()
	return d.x, d.y
}

// roomKey creates a new room entity key.
func roomKey(c appengine.Context) *datastore.Key {
	return datastore.NewKey(c, "Room", "default_room", 0, nil)
}

// dieKey creates a new die entity key.
func dieKey(c appengine.Context, roomKey *datastore.Key) *datastore.Key {
	return datastore.NewKey(c, "Die", "", 0, roomKey)
}

func newRoom(c appengine.Context) (string, error) {
	k, err := datastore.Put(c, roomKey(c), &Room{})
	if err != nil {
		return "", fmt.Errorf("could not create new room: %v", err)
	}
	return k.Encode(), nil
}

func newDie(c appengine.Context, size string, roomKey *datastore.Key) error {
	r, rs := getNewResult(size)
	diu, err := getDieImageURL(c, size, rs)
	if err != nil {
		log.Errorf(context.Background(), "could not get die image url: %v", err)
	}
	d := Die{
		Size:      size,
		Result:    r,
		ResultStr: rs,
		Key:       dieKey(c, roomKey),
		Timestamp: time.Now(),
		Image:     diu.String(),
	}
	_, err = datastore.Put(c, d.Key, &d)
	if err != nil {
		return fmt.Errorf("could not create new die: %v", err)
	}
	return nil
}

func getRoomDice(c appengine.Context, encodedRoomKey string) ([]Die, error) {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return nil, fmt.Errorf("could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).Order("Result")
	dice := []Die{}
	if _, err = q.GetAll(c, &dice); err != nil {
		return nil, fmt.Errorf("problem executing dice query: %v", err)
	}
	return dice, nil
}

func clearRoomDice(c appengine.Context, encodedRoomKey string) error {
	k, err := datastore.DecodeKey(encodedRoomKey)
	if err != nil {
		return fmt.Errorf("could not decode room key %v: %v", encodedRoomKey, err)
	}
	q := datastore.NewQuery("Die").Ancestor(k).KeysOnly()
	out := q.Run(c)
	for {
		d, err := out.Next(nil)
		if err != nil {
			break
		}
		err = datastore.Delete(c, d)
		if err != nil {
			log.Errorf(context.Background(), "problem deleting dice: %v", err)
		}
	}
	return nil
}

func getDieImageURL(c appengine.Context, size, result string) (*url.URL, error) {
	d := fmt.Sprintf("d%s/%s.png", size, result)
	// Should this have a mutex?
	if u, ok := diceURLs[d]; ok {
		return u, nil
	}
	path := fmt.Sprintf("//gs/visual-dice-roller/die_images/%s", d)
	k, err := blobstore.BlobKeyForFile(c, path)
	if err != nil {
		return nil, fmt.Errorf("could not find image: %v", err)
	}
	u, err := image.ServingURL(c, k, nil)
	if err != nil {
		return nil, fmt.Errorf("could not generate image url: %v", err)
	}
	diceURLs[d] = u
	return u, nil
}

func getDeleteImageURL(c appengine.Context) (*url.URL, error) {
	d := "delete.png"
	// Should this have a mutex?
	if u, ok := diceURLs[d]; ok {
		return u, nil
	}
	path := fmt.Sprintf("//gs/visual-dice-roller/die_images/%s", d)
	k, err := blobstore.BlobKeyForFile(c, path)
	if err != nil {
		return nil, fmt.Errorf("could not find image: %v", err)
	}
	u, err := image.ServingURL(c, k, nil)
	if err != nil {
		return nil, fmt.Errorf("could not generate image url: %v", err)
	}
	diceURLs[d] = u
	return u, nil
}

func updateDieLocation(c appengine.Context, encodedDieKey string, x, y int) error {
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
	return nil
}

func getNewResult(kind string) (int, string) {
	rand.Seed(int64(time.Now().Nanosecond()))
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

// the root request should check for a cookie which tells what room the request should actually go to
// (reminder: when setting the room explicitly, set the cookie) - if that cookie is not present or is
// invalid, create a new random room, set the cookie and drop the user there, otherwise show user
// correct room.
func root(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err == nil {
		http.Redirect(w, r, fmt.Sprintf("/room?id=%v", roomCookie.Value), http.StatusFound)
	}
	// If no cookie, then create a room, set cookie, and redirect
	room, err := newRoom(c)
	if err != nil {
		// TODO(shanel): This should probably say something more...
		http.NotFound(w, r)
	}
	http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room})
	http.Redirect(w, r, fmt.Sprintf("/room?id=%v", room), http.StatusFound)
}

func room(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	room := r.URL.Query()["id"][0] // is this going to break?
	dice, err := getRoomDice(c, room)
	if err != nil {
		// Can probably nuke this once done testing - it'll spam the logs
		log.Errorf(context.Background(), "could not get dice for room %v", room)
	}
	// now we need a template for the whole page, and in the short term just print out strings of dice

	if err := roomTemplate.Execute(w, dice); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

var roomTemplate = template.Must(template.New("room").Parse(`
<html>
  <head>
    <title>Dice Roller</title>
  </head>
  <body>
    <p><b>Results:</b></p>
    <hr>
    {{range .}}
      <p><b>d{{.Size}}</b>: {{.ResultStr}} <img src="{{.Image}}"></p>
    {{end}}
    <form action="/roll" method="post">
      <div><textarea name="d4" rows="1" cols="2"></textarea>d4</div>
      <div><textarea name="d6" rows="1" cols="2"></textarea>d6</div>
      <div><textarea name="d8" rows="1" cols="2"></textarea>d8</div>
      <div><textarea name="d10" rows="1" cols="2"></textarea>d10</div>
      <div><textarea name="d12" rows="1" cols="2"></textarea>d12</div>
      <div><textarea name="d20" rows="1" cols="2"></textarea>d20</div>
      <div><textarea name="dF" rows="1" cols="2"></textarea>dF</div>
      <div><input type="submit" value="Roll"></div>
    </form>
    <form action="clear" method="post">
      <div><input type="submit" value="Clear"></div>
    </form>
  </body>
</html>
`))

// the roll request should either have an explicit room in the passed args or be in the cookie, it
// should then generate all the dice (results) and make sure they line up properly (and don't overlap
// already existing dice).
func roll(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	ctx := context.Background()
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err != nil {
		// If no cookie, then create a room, set cookie, and redirect
		room, err := newRoom(c)
		if err != nil {
			// TODO(shanel): This should probably say something more...
			log.Errorf(ctx, "room problem: %v", err)
			http.NotFound(w, r)
		}
		http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room})
		http.Redirect(w, r, fmt.Sprintf("/room?id=%v", room), http.StatusFound)
	}
	// Eventually split these all into separate go routines
	roomKey, err := datastore.DecodeKey(roomCookie.Value)
	if err != nil {
		log.Errorf(ctx, "error decoding room key: %v", err)
		http.NotFound(w, r)
	}
	toRoll := map[string]string{
		"4":  r.FormValue("d4"),
		"6":  r.FormValue("d6"),
		"8":  r.FormValue("d4"),
		"10": r.FormValue("d4"),
		"12": r.FormValue("d4"),
		"20": r.FormValue("d4"),
		"F":  r.FormValue("d4"),
	}
	for k, v := range toRoll {
		count, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		for i := 0; i < count; i++ {
			if err = newDie(c, k, roomKey); err != nil {
				log.Errorf(ctx, "problem getting new die: %v", err)
			}
		}

	}
	http.Redirect(w, r, fmt.Sprintf("/room?id=%v", roomCookie.Value), http.StatusFound)
}

func clear(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	ctx := context.Background()
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err != nil {
		// If no cookie, then create a room, set cookie, and redirect
		room, err := newRoom(c)
		if err != nil {
			// TODO(shanel): This should probably say something more...
			log.Errorf(ctx, "room problem: %v", err)
			http.NotFound(w, r)
		}
		http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room})
		http.Redirect(w, r, fmt.Sprintf("/room?id=%v", room), http.StatusFound)
	}
	// Eventually split these all into separate go routines
	err = clearRoomDice(c, roomCookie.Value)
	if err != nil {
		log.Errorf(ctx, "issues clearing dice: %v", err)
	}
	http.Redirect(w, r, fmt.Sprintf("/room?id=%v", roomCookie.Value), http.StatusFound)
}

// get all the room's dice and return them as a slice
// if the room name is unknown return a new room

// take the form's entries and return a slice of dice

// delete a die from the room

// delete all dice in the room

// create a new room, returning it's name

// update a die's position

// create die images(?)

// TODO(shanel): make use of url shortener?
