package roller

import (
	"fmt"
	"html/template"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"appengine"
	"appengine/datastore"
	"appengine/file"
	// Maybe use this later?
	//"appengine/user"
)

type update struct {
	timestamp int64
	updater string
}

// TODO(shanel): Would it make sense to have room updates for #refreshable
// and dice updates for individual dice? maybe just refresh the dice's divs
// as they are updated?
type roomUpdates struct {
	sync.Mutex
//	m map[string]int64
	m map[string]map[string]*update
}

func (r *roomUpdates) updated(room, die, fp string) {
	r.Lock()
	defer r.Unlock()
	if _, ok := r.m[room]; !ok {
//		r.m[room] = map[string]int64{die: time.Now().Unix()}
		r.m[room] = map[string]*update{die: &update{time.Now().Unix(), fp}}
		return
	}
	r.m[room][die] = &update{time.Now().Unix(), fp}
}

func (r *roomUpdates) refresh(room, fp string, s int64) string {
	r.Lock()
	defer r.Unlock()
	remove := []string{}
	if updates, ok := r.m[room]; ok {
		out := make([]string, 0, len(updates))
		for u, t := range updates {
			if (time.Now().Unix() - t.timestamp) < s {
				if t.updater == fp {
					continue
				}
				if u == room {
					return room
				}
				out = append(out, u)
			} else {
				remove = append(remove, u)
			}
		}
		for _, r := range remove {
			delete(updates, r)
		}
		return strings.Join(out, "|")
	}
	return ""
}

func init() {
	http.HandleFunc("/", root)
	http.HandleFunc("/room", room)
	http.HandleFunc("/roll", roll)
	http.HandleFunc("/clear", clear)
	http.HandleFunc("/move", move)
	http.HandleFunc("/refresh", refresh)
}

// As we create urls for the die images, store them here so we don't keep making them
var diceURLs = map[string]string{}
var dieCounter int64
var roomCounter int64
var updates = roomUpdates{m: map[string]map[string]*update{}}

type Room struct{}

type Die struct {
	//	sync.RWMutex
	Size      string // for fate dice this won't be an integer
	Result    int    // For fate dice make this one of three very large numbers?
	ResultStr string
	Color     string
	Label     string
	X         float64
	Y         float64
	Key       *datastore.Key
	KeyStr    string
	Timestamp time.Time
	Image     string
}

func (d *Die) updatePosition(x, y float64) {
	//	d.Lock()
	//	defer d.Unlock()
	d.X = x
	d.Y = y
}

func (d *Die) getPosition() (float64, float64) {
	//	d.RLock()
	//	defer d.RUnlock()
	return d.X, d.Y
}

// roomKey creates a new room entity key.
func roomKey(c appengine.Context) *datastore.Key {
	roomCounter++
	return datastore.NewKey(c, "Room", "", roomCounter, nil)
}

// dieKey creates a new die entity key.
func dieKey(c appengine.Context, roomKey *datastore.Key) *datastore.Key {
	dieCounter++
	return datastore.NewKey(c, "Die", "", dieCounter, roomKey)
}

// TODO(shanel): Have a button to create a new room
func newRoom(c appengine.Context) (string, error) {
	k, err := datastore.Put(c, roomKey(c), &Room{})
	if err != nil {
		return "", fmt.Errorf("could not create new room: %v", err)
	}
	return k.Encode(), nil
}

// TODO(shanel): After anything that changes the room, update the roomUpdates map so clients can check
// in to see if they should refresh
func newRoll(c appengine.Context, sizes map[string]string, roomKey *datastore.Key, length int) error {
	dice := make([]*Die, 0, length)
	keys := make([]*datastore.Key, 0, length)
	for size, v := range sizes {
		count, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		for i := 0; i < count; i++ {
			r, rs := getNewResult(size)
			diu, err := getDieImageURL(c, size, rs)
			if err != nil {
				c.Infof("could not get die image: %v", err)
			}
			dk := dieKey(c, roomKey)
			d := Die{
				Size:      size,
				Result:    r,
				ResultStr: rs,
				Key:       dk,
				KeyStr:    dk.Encode(),
				Timestamp: time.Now(),
				Image:     diu,
			}
			dice = append(dice, &d)
			keys = append(keys, dk)
		}
	}
	keyStrings := []string{}
	for _, k := range keys {
		keyStrings = append(keyStrings, k.Encode())
	}
	rk, err := datastore.PutMulti(c, keys, dice)
	if err != nil {
		return fmt.Errorf("could not create new dice: %v", err)
	}
	if len(rk) != len(keys) {
		return fmt.Errorf("wrong number of keys returned: got %v, want %v", len(rk), len(keys))
	}
	return nil
}

func newDie(c appengine.Context, size string, roomKey *datastore.Key) error {
	r, rs := getNewResult(size)
	diu, err := getDieImageURL(c, size, rs)
	if err != nil {
		return fmt.Errorf("could not get die image: %v", err)
	}
	d := Die{
		Size:      size,
		Result:    r,
		ResultStr: rs,
		Key:       dieKey(c, roomKey),
		Timestamp: time.Now(),
		Image:     diu,
	}
	// TODO(shanel): Refactor to use PutMulti instead
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
	q := datastore.NewQuery("Die").Ancestor(k).Order("Result").Limit(10)
	dice := make([]Die, 0, 10)
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
		// TODO(shanel): Refactor to use DeleteMulti
		err = datastore.Delete(c, d)
		if err != nil {
			return fmt.Errorf("problem deleting room dice: %v", err)
		}
	}
	return nil
}

func getDieImageURL(c appengine.Context, size, result string) (string, error) {
	// Fate dice silliness
	ft := map[string]string{"-": "minus", "+": "plus", " ": "zero"}
	if _, ok := ft[result]; ok {
		result = ft[result]
	}
	d := fmt.Sprintf("d%s/%s.png", size, result)
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

func getDeleteImageURL(c appengine.Context) (string, error) {
	d := "delete.png"
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

func updateDieLocation(c appengine.Context, encodedDieKey string, x, y float64) error {
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
	rand.Seed(int64(time.Now().Unix()))
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	// now we need a template for the whole page, and in the short term just print out strings of dice

	cookie := &http.Cookie{Name: "dice_room", Value: room}
	http.SetCookie(w, cookie)
	if err := roomTemplate.Execute(w, dice); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// TODO(shanel): Probably would make the most sense to try and push all updates here (ie not just rooms)
func refresh(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr := r.Form.Get("id")
	fp := r.Form.Get("fp")
	ref := updates.refresh(keyStr, fp, 1)
	c.Infof("checking refresh for %v, (fp: %v): %v", keyStr, fp, ref)
	fmt.Fprintf(w, "%v", ref)
}

func move(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	r.ParseForm()
	keyStr := r.Form.Get("id")
	fp := r.Form.Get("fp")
	x, err := strconv.ParseFloat(r.Form.Get("x"), 64)
	if err != nil {
		c.Errorf("quietly not updating position of %v: %v", keyStr, err)
	}
	y, err := strconv.ParseFloat(r.Form.Get("y"), 64)
	if err != nil {
		c.Errorf("quietly not updating position of %v: %v", keyStr, err)
	}
	err = updateDieLocation(c, keyStr, x, y)
	if err != nil {
		c.Errorf("quietly not updating position of %v to (%v, %v): %v", keyStr, x, y, err)
	}
	roomCookie, err := r.Cookie("dice_room")
	if err == nil {
		http.Redirect(w, r, fmt.Sprintf("/room?id=%v", roomCookie.Value), http.StatusFound)
	}
	updates.updated(roomCookie.Value, keyStr, fp)
}

// Unexpected token at start of html?

var roomTemplate = template.Must(template.New("room").Parse(`
<html>
  <head>
    <title>Dice Roller</title>
  <link rel="stylesheet" type="text/css" src="css/drag.css" />
  <!-- <script src="js/client.js"></script> -->
  <script src="http://code.interactjs.io/v1.2.9/interact.js"></script>
  <script src="https://ajax.googleapis.com/ajax/libs/jquery/3.2.1/jquery.min.js"></script>
<script type="text/javascript" language="javascript">



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

    // call this function on every dragmove event
    onmove: dragMoveListener,
    onend: dragMoveEnd,
    // call this function on every dragend event
//    onend: function (event) {
//      var textEl = event.target.querySelector('p');
//
//      textEl && (textEl.textContent =
//        'moved a distance of '
//        + (Math.sqrt(event.dx * event.dx +
//                     event.dy * event.dy)|0) + 'px');
//    }
  });
  function dragMoveEnd (event) {
    var target = event.target,
        // keep the dragged position in the data-x/data-y attributes
        x = (parseFloat(target.getAttribute('data-x')) || 0),
        y = (parseFloat(target.getAttribute('data-y')) || 0);



    // translate the element
    target.style.webkittransform =
    target.style.transform =
      'translate(' + x + 'px, ' + y + 'px)';

    // update the posiion attributes
    target.setAttribute('data-x', x);
    target.setAttribute('data-y', y);

    $.post('move', {'id': target.id, 'x': x, 'y': y, 'fp': fingerprint});

  }

  function dragMoveListener (event) {
    var target = event.target,
        // keep the dragged position in the data-x/data-y attributes
        x = (parseFloat(target.getAttribute('data-x')) || 0) + event.dx,
        y = (parseFloat(target.getAttribute('data-y')) || 0) + event.dy;



    // translate the element
    target.style.webkittransform =
    target.style.transform =
      'translate(' + x + 'px, ' + y + 'px)';

    // update the posiion attributes
    target.setAttribute('data-x', x);
    target.setAttribute('data-y', y);


  }

  // this is used later in the resizing and gesture demos
  window.dragMoveListener = dragMoveListener;

/* The dragging code for '.draggable' from the demo above
 * applies to this demo as well so it doesn't have to be repeated. */

// enable draggables to be dropped into this
interact('.dropzone').dropzone({
  // only accept elements matching this CSS selector
  accept: '#yes-drop',
  // Require a 75% element overlap for a drop to be possible
  overlap: 0.75,

  // listen for drop related events:

  ondropactivate: function (event) {
    // add active dropzone feedback
    event.target.classList.add('drop-active');
  },
  ondragenter: function (event) {
    var draggableElement = event.relatedTarget,
        dropzoneElement = event.target;

    // feedback the possibility of a drop
    dropzoneElement.classList.add('drop-target');
    draggableElement.classList.add('can-drop');
    draggableElement.textContent = 'Dragged in';
  },
  ondragleave: function (event) {
    // remove the drop feedback style
    event.target.classList.remove('drop-target');
    event.relatedTarget.classList.remove('can-drop');
    event.relatedTarget.textContent = 'Dragged out';
  },
  ondrop: function (event) {
    event.relatedTarget.textContent = 'Dropped';
  },
  ondropdeactivate: function (event) {
    // remove active dropzone feedback
    event.target.classList.remove('drop-active');
    event.target.classList.remove('drop-target');
  }
});

var client = new ClientJS();
var fingerprint = client.getFingerprint();
$.cookie("dice_roller_fp", fingerprint);

 function autoRefresh_div() {
     var room = (window.location.href).split("=")[1];
     $.post("/refresh", {
             id: room,
	     fp: fingerprint,
         })
         .done(function(data) {
		 // need to cycle through the |-spearated list
             var b = data;
//             var item_str = data;
//	     var items = item_str.split("|");
//	     for (i = 0; i < items.length; i++) {
//		     if (items[0] == "") {
//			     break
//		     }
//		     console.log(items[i]);
//		     $("#" + items[i]).load(window.location.href + " #" + items[i]);
//	     };
             var t = 'true';
//             console.log(b);
//             console.log(t);
             if (b != "") {
                 $("#refreshable").load(window.location.href + " #refreshable");
             };
         });
 }
 
  setInterval('autoRefresh_div()', 1000); // refresh div after 1 second
  </script>
  </head>
  <body>
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
    <form action="/clear" method="post">
      <div><input type="submit" value="Clear"></div>
    </form>
    <p><b>Results:</b></p>
    <hr>
    <div id="refreshable">
    {{range .}}
      <div id="{{.KeyStr}}" class="draggable" data-x="{{.X}}" data-y="{{.Y}}" style="transform: translate({{.X}}px, {{.Y}}px);">
        <img src="{{.Image}}">
      </div>
    {{end}}
    <br>
    <br>
<div id="drag-1" class="draggable">
  <p> You can drag one element </p>
</div>
<div id="drag-2" class="draggable">
    <p> with each pointer </p>
</div>
<br>
<div id="no-drop" class="draggable drag-drop"> #no-drop </div>

<div id="yes-drop" class="draggable drag-drop"> #yes-drop </div>

<div id="outer-dropzone" class="dropzone">
  #outer-dropzone
  <div id="inner-dropzone" class="dropzone">#inner-dropzone</div>
 </div>
    </div>
  </body>
</html>
`))

// the roll request should either have an explicit room in the passed args or be in the cookie, it
// should then generate all the dice (results) and make sure they line up properly (and don't overlap
// already existing dice).
func roll(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err != nil {
		// If no cookie, then create a room, set cookie, and redirect
		room, err := newRoom(c)
		if err != nil {
			// TODO(shanel): This should probably say something more...
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		cookie := &http.Cookie{Name: "dice_room", Value: room}
		http.SetCookie(w, cookie)
		roomCookie = cookie
	}
	// Eventually split these all into separate go routines
	roomKey, err := datastore.DecodeKey(roomCookie.Value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	toRoll := map[string]string{
		"4":  r.FormValue("d4"),
		"6":  r.FormValue("d6"),
		"8":  r.FormValue("d8"),
		"10": r.FormValue("d10"),
		"12": r.FormValue("d12"),
		"20": r.FormValue("d20"),
		"F":  r.FormValue("dF"),
	}
	var counter int
	for _, _ = range toRoll {
		counter++
	}
	//	for k, v := range toRoll {
	//		count, err := strconv.Atoi(v)
	//		if err != nil {
	//			continue
	//		}
	//		for i := 0; i < count; i++ {
	if err = newRoll(c, toRoll, roomKey, counter); err != nil {
		c.Errorf("%v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	//		}
	//
	//	}
	fp, err := r.Cookie("dice_roller_fp")
	if err != nil {
		c.Errorf("couldn't find fingerprint")
	}
	updates.updated(roomKey.Encode(), roomKey.Encode(), fp.Value)
	http.Redirect(w, r, fmt.Sprintf("/room?id=%v", roomCookie.Value), http.StatusFound)
}

func clear(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// Check for cookie based room
	roomCookie, err := r.Cookie("dice_room")
	if err != nil {
		// If no cookie, then create a room, set cookie, and redirect
		room, err := newRoom(c)
		if err != nil {
			// TODO(shanel): This should probably say something more...
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		http.SetCookie(w, &http.Cookie{Name: "dice_room", Value: room})
		http.Redirect(w, r, fmt.Sprintf("/room?id=%v", room), http.StatusFound)
	}
	// Eventually split these all into separate go routines
	err = clearRoomDice(c, roomCookie.Value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fp, err := r.Cookie("dice_roller_fp")
	if err != nil {
		c.Errorf("couldn't find fingerprint")
	}
	updates.updated(roomCookie.Value, roomCookie.Value, fp.Value)
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
