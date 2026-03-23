package emoji

import (
	"sort"
	"strings"
)

// Entry is a single emoji with searchable metadata.
type Entry struct {
	Char     string
	Name     string
	Keywords string // space-separated search terms
}

// Search returns up to limit emojis matching the query.
// An empty query returns the first limit entries (popular picks).
func Search(query string, limit int) []Entry {
	if limit <= 0 {
		limit = 10
	}
	if strings.TrimSpace(query) == "" {
		if limit > len(catalog) {
			limit = len(catalog)
		}
		return catalog[:limit]
	}
	words := strings.Fields(strings.ToLower(query))
	type scored struct {
		entry Entry
		score int
	}
	var results []scored
	for _, e := range catalog {
		haystack := strings.ToLower(e.Name + " " + e.Keywords)
		s := 0
		for _, w := range words {
			if strings.Contains(haystack, w) {
				s++
				// bonus for word-boundary match
				if strings.HasPrefix(haystack, w) || strings.Contains(haystack, " "+w) {
					s++
				}
			}
		}
		if s > 0 {
			results = append(results, scored{e, s})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].entry.Name < results[j].entry.Name
	})
	out := make([]Entry, 0, limit)
	for i := 0; i < len(results) && i < limit; i++ {
		out = append(out, results[i].entry)
	}
	return out
}

var catalog = []Entry{
	// ── Smileys & Emotion ──
	{"😀", "grinning face", "happy smile joy grin"},
	{"😃", "smiley", "happy smile joy"},
	{"😄", "smile", "happy grin joy laugh"},
	{"😁", "beaming face", "grin happy teeth"},
	{"😆", "laughing", "happy haha lol squint"},
	{"😅", "sweat smile", "nervous relief hot"},
	{"🤣", "rofl", "laugh floor rolling lmao"},
	{"😂", "tears of joy", "laugh cry lol funny"},
	{"🙂", "slight smile", "ok fine"},
	{"🙃", "upside down", "sarcasm silly"},
	{"😉", "wink", "flirt nudge"},
	{"😊", "blush", "happy shy sweet"},
	{"😇", "angel", "innocent halo good"},
	{"🥰", "love face", "hearts adore affection"},
	{"😍", "heart eyes", "love crush adore"},
	{"🤩", "star struck", "amazing wow excited"},
	{"😘", "kiss", "love smooch mwah"},
	{"😋", "yummy", "delicious tasty tongue food"},
	{"😛", "tongue out", "silly playful"},
	{"😜", "crazy wink", "silly goofy tongue"},
	{"🤪", "zany", "wild silly crazy goofy"},
	{"🤗", "hug", "embrace warm open"},
	{"🤔", "thinking", "hmm consider ponder"},
	{"🫡", "salute", "respect yes sir"},
	{"🤫", "shush", "quiet secret silence"},
	{"🤐", "zipper mouth", "shut quiet secret mute"},
	{"🤨", "raised eyebrow", "suspicious doubt skeptic"},
	{"😐", "neutral", "meh blank straight"},
	{"😑", "expressionless", "blank meh unamused"},
	{"😶", "no mouth", "silent speechless mute"},
	{"😏", "smirk", "sly flirt smug"},
	{"😒", "unamused", "annoyed bored meh"},
	{"🙄", "eye roll", "whatever annoyed please"},
	{"😬", "grimace", "awkward nervous yikes"},
	{"😌", "relieved", "calm peaceful content zen"},
	{"😔", "pensive", "sad thoughtful disappointed"},
	{"😪", "sleepy", "tired tear"},
	{"😴", "sleeping", "zzz tired rest nap"},
	{"🤢", "nauseated", "sick green gross disgusted"},
	{"🤮", "vomit", "sick puke barf gross"},
	{"🤯", "exploding head", "mind blown shocked wow"},
	{"🤠", "cowboy", "hat western yeehaw"},
	{"🥳", "party face", "celebrate birthday horn hat"},
	{"😎", "sunglasses", "cool dude chill rad"},
	{"🤓", "nerd", "geek glasses smart"},
	{"🧐", "monocle", "inspect investigate curious"},
	{"😕", "confused", "unsure puzzled"},
	{"😟", "worried", "nervous anxious concern"},
	{"😮", "surprised", "open mouth oh gasp"},
	{"😱", "scream", "fear horror shock omg"},
	{"😢", "cry", "sad tear upset"},
	{"😭", "sob", "cry sad bawl wail tears"},
	{"😤", "steam nose", "angry frustrated huff"},
	{"😡", "rage", "angry furious mad"},
	{"🤬", "cursing", "swear angry symbols expletive"},
	{"😈", "devil smile", "evil mischief naughty"},
	{"🥲", "smile tear", "happy sad bittersweet"},
	{"🫠", "melting", "hot dissolve disappear"},
	{"🫣", "peeking", "shy cover eye"},
	{"🫢", "open eyes hand", "oops gasp surprise"},

	// ── Gestures & Body ──
	{"👋", "wave", "hi hello bye hand"},
	{"👍", "thumbs up", "yes good ok approve like"},
	{"👎", "thumbs down", "no bad dislike disapprove"},
	{"👏", "clap", "bravo applause congrats"},
	{"🙌", "raised hands", "hooray celebration praise"},
	{"🤝", "handshake", "deal agree partnership"},
	{"🙏", "pray", "please thanks hope namaste"},
	{"✌️", "peace", "victory two"},
	{"🤞", "crossed fingers", "luck hope wish"},
	{"🤟", "love you", "hand sign ily"},
	{"🤘", "rock", "metal horns"},
	{"🤙", "call me", "phone hang loose shaka"},
	{"👆", "point up", "this above"},
	{"👇", "point down", "this below"},
	{"👈", "point left", "this way"},
	{"👉", "point right", "this way"},
	{"🖕", "middle finger", "flip off rude"},
	{"✊", "fist", "power solidarity punch"},
	{"👊", "fist bump", "punch bro"},
	{"💪", "flexed bicep", "strong muscle power gym"},
	{"🧠", "brain", "smart think intelligent mind"},
	{"👀", "eyes", "look see watch stare"},
	{"👁️", "eye", "look see watch"},
	{"👄", "mouth", "lips kiss"},
	{"🫶", "heart hands", "love appreciate"},

	// ── Hearts ──
	{"❤️", "red heart", "love"},
	{"🧡", "orange heart", "love"},
	{"💛", "yellow heart", "love friendship"},
	{"💚", "green heart", "love nature"},
	{"💙", "blue heart", "love trust"},
	{"💜", "purple heart", "love"},
	{"🖤", "black heart", "dark love goth"},
	{"🤍", "white heart", "pure love"},
	{"💔", "broken heart", "sad heartbreak"},
	{"❤️‍🔥", "heart fire", "passion desire lust"},
	{"💕", "two hearts", "love couple"},
	{"💖", "sparkling heart", "love shiny"},
	{"💗", "growing heart", "love affection"},
	{"💘", "heart arrow", "love cupid valentine"},

	// ── People & Fantasy ──
	{"💀", "skull", "dead death skeleton rip"},
	{"☠️", "skull crossbones", "danger death pirate poison"},
	{"💩", "poop", "shit crap turd"},
	{"🤖", "robot", "bot machine ai android"},
	{"👻", "ghost", "boo halloween spooky"},
	{"👽", "alien", "ufo space extraterrestrial"},
	{"👾", "space invader", "game alien arcade pixel"},
	{"🤡", "clown", "circus funny"},
	{"😈", "imp", "devil evil mischief"},
	{"👿", "angry devil", "evil demon"},
	{"🎅", "santa", "christmas holiday xmas"},

	// ── Animals ──
	{"🐶", "dog", "puppy pet woof"},
	{"🐱", "cat", "kitten pet meow"},
	{"🐭", "mouse", "rat squeak"},
	{"🐰", "rabbit", "bunny easter"},
	{"🦊", "fox", "sly clever"},
	{"🐻", "bear", "teddy grizzly"},
	{"🐼", "panda", "bear bamboo"},
	{"🐨", "koala", "australia bear"},
	{"🐯", "tiger", "rawr stripes"},
	{"🦁", "lion", "king jungle roar"},
	{"🐮", "cow", "moo milk"},
	{"🐷", "pig", "oink pork"},
	{"🐸", "frog", "ribbit toad"},
	{"🐵", "monkey", "banana ape primate"},
	{"🐔", "chicken", "hen rooster"},
	{"🐧", "penguin", "bird ice cold tux linux"},
	{"🦆", "duck", "quack bird"},
	{"🦅", "eagle", "bird america freedom"},
	{"🦉", "owl", "bird wise night hoot"},
	{"🐍", "snake", "python hiss reptile"},
	{"🐢", "turtle", "slow tortoise shell"},
	{"🐙", "octopus", "tentacle sea"},
	{"🦈", "shark", "fish predator"},
	{"🐋", "whale", "ocean sea big"},
	{"🦋", "butterfly", "insect pretty metamorphosis"},
	{"🐝", "bee", "honey buzz insect"},
	{"🐞", "ladybug", "insect lucky"},
	{"🦄", "unicorn", "magic horse rainbow fantasy"},

	// ── Food & Drink ──
	{"🍎", "apple", "fruit red"},
	{"🍕", "pizza", "food slice cheese pepperoni"},
	{"🍔", "hamburger", "burger food beef"},
	{"🌮", "taco", "mexican food"},
	{"🍟", "french fries", "food fast mcdonalds"},
	{"🌭", "hot dog", "food sausage"},
	{"🍿", "popcorn", "movie snack"},
	{"🍩", "donut", "doughnut sweet dessert"},
	{"🍪", "cookie", "sweet biscuit dessert"},
	{"🎂", "birthday cake", "party dessert celebrate"},
	{"🍰", "cake slice", "dessert sweet pie"},
	{"🍫", "chocolate", "candy sweet bar"},
	{"🍬", "candy", "sweet sugar"},
	{"🍭", "lollipop", "candy sweet"},
	{"🍦", "ice cream", "dessert sweet cold cone"},
	{"☕", "coffee", "hot drink espresso cafe morning"},
	{"🍵", "tea", "hot drink green matcha"},
	{"🍺", "beer", "drink alcohol brew pint"},
	{"🍷", "wine", "drink alcohol glass red white"},
	{"🍸", "cocktail", "drink martini alcohol bar"},
	{"🥃", "whiskey", "drink alcohol tumbler bourbon"},
	{"🍾", "champagne", "celebrate drink bottle pop"},
	{"🧃", "juice box", "drink"},
	{"🥤", "soda", "drink cup straw"},
	{"🧋", "boba", "bubble tea drink milk"},

	// ── Nature & Weather ──
	{"🌸", "cherry blossom", "flower spring pink sakura"},
	{"🌹", "rose", "flower love red romance"},
	{"🌻", "sunflower", "flower yellow sun"},
	{"🍀", "four leaf clover", "luck lucky irish shamrock"},
	{"🌲", "evergreen", "tree pine forest"},
	{"🌴", "palm tree", "tropical beach vacation"},
	{"🍄", "mushroom", "fungus toad mario"},
	{"🔥", "fire", "hot flame lit burn trending"},
	{"💧", "droplet", "water tear rain drop"},
	{"🌊", "wave", "ocean sea water surf"},
	{"⭐", "star", "favorite bookmark yellow"},
	{"✨", "sparkles", "magic glitter shine special clean"},
	{"⚡", "lightning", "zap electric bolt thunder power"},
	{"❄️", "snowflake", "cold winter ice frozen"},
	{"🌈", "rainbow", "colors pride spectrum"},
	{"☀️", "sun", "sunny bright hot weather"},
	{"🌙", "moon", "night crescent sleep"},
	{"☁️", "cloud", "weather overcast"},
	{"🌧️", "rain", "weather cloud water"},
	{"💨", "wind", "blow dash fast"},

	// ── Objects & Tech ──
	{"💻", "laptop", "computer tech code programming"},
	{"🖥️", "desktop", "computer screen monitor"},
	{"⌨️", "keyboard", "type computer input"},
	{"📱", "phone", "mobile cell smartphone"},
	{"💡", "lightbulb", "idea bright thought innovation"},
	{"🔋", "battery", "power charge energy"},
	{"📷", "camera", "photo picture"},
	{"🎧", "headphones", "music audio listen"},
	{"🎤", "microphone", "sing karaoke record"},
	{"🎸", "guitar", "music rock instrument"},
	{"📝", "memo", "note write document"},
	{"✏️", "pencil", "write edit draw"},
	{"📌", "pin", "location pushpin mark"},
	{"🔒", "lock", "secure private closed"},
	{"🔓", "unlock", "open access"},
	{"🔑", "key", "password access unlock"},
	{"🔨", "hammer", "tool build construct"},
	{"🔧", "wrench", "tool fix repair"},
	{"⚙️", "gear", "settings config cog mechanical"},
	{"🛠️", "tools", "build fix repair hammer wrench"},
	{"💰", "money bag", "rich cash dollar wealth"},
	{"💳", "credit card", "payment buy purchase"},
	{"💎", "gem", "diamond jewel precious shiny"},
	{"🎁", "gift", "present birthday wrapped"},
	{"🏆", "trophy", "winner champion award first"},
	{"🥇", "gold medal", "first winner champion"},
	{"📦", "package", "box delivery shipping"},
	{"🗑️", "trash", "delete garbage bin waste"},
	{"💣", "bomb", "boom explode danger"},
	{"🔗", "link", "chain url connection"},
	{"📎", "paperclip", "attachment clip"},
	{"🔔", "bell", "notification alert ring"},
	{"🔕", "bell off", "mute silent notification"},
	{"📡", "satellite", "signal broadcast antenna"},
	{"🧲", "magnet", "attract pull"},
	{"🪄", "magic wand", "wizard spell"},
	{"🧪", "test tube", "science lab experiment"},
	{"💊", "pill", "medicine drug health"},
	{"🩹", "bandage", "heal fix patch"},

	// ── Symbols & UI ──
	{"✅", "check", "done yes complete ok green"},
	{"❌", "cross mark", "no wrong delete remove red"},
	{"❓", "question", "what help confused"},
	{"❗", "exclamation", "alert important warning"},
	{"⚠️", "warning", "caution alert danger"},
	{"🔴", "red circle", "stop error"},
	{"🟢", "green circle", "go online active"},
	{"🔵", "blue circle", "info"},
	{"🟡", "yellow circle", "pending caution"},
	{"🟣", "purple circle", ""},
	{"⚫", "black circle", ""},
	{"⚪", "white circle", ""},
	{"🚫", "prohibited", "no ban forbidden block"},
	{"💯", "hundred", "perfect score full max"},
	{"♻️", "recycle", "green environment reuse"},
	{"🏳️", "white flag", "surrender peace"},
	{"🏴", "black flag", "pirate"},
	{"🚩", "red flag", "warning danger alert"},
	{"➡️", "right arrow", "next forward"},
	{"⬆️", "up arrow", "above top"},
	{"⬇️", "down arrow", "below bottom"},
	{"⬅️", "left arrow", "back previous"},
	{"↩️", "return", "back undo reply"},
	{"🔄", "refresh", "reload sync update cycle"},
	{"ℹ️", "info", "information about"},
	{"🆕", "new", "fresh just"},
	{"🆗", "ok", "fine sure"},
	{"🔝", "top", "best above"},
	{"🔜", "soon", "coming near"},
	{"✳️", "asterisk", "star symbol"},
	{"💬", "speech bubble", "chat talk message comment"},
	{"💭", "thought bubble", "think dream cloud"},
	{"🗣️", "speaking head", "talk voice loud"},

	// ── Activities & Transport ──
	{"🚀", "rocket", "launch ship fast space startup"},
	{"✈️", "airplane", "flight travel fly"},
	{"🚗", "car", "drive vehicle automobile"},
	{"🏠", "house", "home building"},
	{"🌍", "globe", "earth world planet international"},
	{"🎮", "controller", "game gaming play video"},
	{"🎯", "target", "bullseye goal aim dart"},
	{"🎲", "dice", "game luck random chance"},
	{"⚽", "soccer", "football sport ball"},
	{"🏀", "basketball", "sport ball hoop"},
	{"🏈", "football", "sport american ball"},
	{"🎾", "tennis", "sport ball racket"},
	{"🏊", "swimming", "sport pool water"},
	{"🚴", "cycling", "bike bicycle ride"},
	{"🎵", "music note", "song melody tune"},
	{"🎶", "music notes", "song melody singing"},
	{"🎬", "clapper", "movie film action cinema"},
	{"🎨", "art palette", "paint draw creative design"},
	{"🎭", "theater", "drama masks acting"},
	{"📚", "books", "read study library learn"},
	{"🎓", "graduation", "school education degree cap"},
	{"🕐", "clock", "time hour"},
	{"⏰", "alarm", "clock time wake morning"},
	{"⏳", "hourglass", "time wait loading sand"},
}
