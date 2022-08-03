package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-colorable"

	"github.com/Tnze/go-mc/bot"
	"github.com/Tnze/go-mc/bot/basic"
	"github.com/Tnze/go-mc/bot/screen"
	"github.com/Tnze/go-mc/chat"
	"github.com/Tnze/go-mc/data/entity"
	"github.com/Tnze/go-mc/data/item"
	_ "github.com/Tnze/go-mc/data/lang/zh-cn"
	zh_cn "github.com/Tnze/go-mc/data/lang/zh-cn"
	pktid "github.com/Tnze/go-mc/data/packetid"
	"github.com/Tnze/go-mc/data/soundid"
	"github.com/Tnze/go-mc/nbt"
	pk "github.com/Tnze/go-mc/net/packet"
)

const timeout = 45

var (
	client        *bot.Client
	player        *basic.Player
	myPosition    Position
	screenManager *screen.Manager

	watch    chan time.Time
	pidCount sync.Map
)

func main() {
	log.SetOutput(colorable.NewColorableStdout())
	var address = flag.String("server", "127.0.0.1:25565", "The server address")
	var user = flag.String("player", "bot", "The player name")
	flag.Parse()

	client = bot.NewClient()
	client.Auth.Name = *user
	player = basic.NewPlayer(client, basic.DefaultSettings)

	//Register event handlers
	basic.EventsListener{
		GameStart:  onGameStart,
		ChatMsg:    onChatMsg,
		Disconnect: onDisconnect,
		Death:      onDeath,
	}.Attach(client)

	screenManager = screen.NewManager(client, screen.EventsListener{
		Open:    nil,
		SetSlot: onScreenSlotChange,
		Close:   nil,
	})

	handlers := map[int32]func(pk.Packet) error{
		pktid.ClientboundAddExperienceOrb: onSpawnOrb,
		pktid.ClientboundSetExperience:    onGotExp,
		pktid.ClientboundAddEntity:        onEntitySpawning,
		pktid.ClientboundSetEntityData:    onEntityData,
		pktid.ClientboundUpdateAttributes: onEntityProperties,
		pktid.ClientboundMoveEntityRot:    onEntityRotate,
		pktid.ClientboundSetSubtitleText:  onSubtitle,
		pktid.ClientboundTakeItemEntity:   onTakeItem,
		pktid.ClientboundSound:            onSound,
		pktid.ClientboundPlayerInfo:       onUpdatePlayerInfo,
		pktid.ClientboundUpdateRecipes:    onUpdateRecipes,
		pktid.ClientboundSetHealth:        onUpdateHealth,
		pktid.ClientboundSetTime:          onUpdateTime,
		pktid.ClientboundPlayerPosition:   onUpdatePosition,
	}

	ignorePid := map[int32]bool{
		pktid.ClientboundKeepAlive:           true,
		pktid.ClientboundContainerSetSlot:    true,
		pktid.ClientboundContainerSetContent: true,
		pktid.ClientboundSetTime:             true,
		pktid.ClientboundSetEquipment:        true,
		pktid.ClientboundEntityEvent:         true, // TODO:
		pktid.ClientboundPlayerAbilities:     true,
		pktid.ClientboundSetEntityMotion:     true,
		pktid.ClientboundAnimate:             true,
		pktid.ClientboundRemoveEntities:      true,
	}

	for id, handler := range handlers {
		id := id
		handler := handler
		ignorePid[id] = true
		client.Events.AddListener(
			bot.PacketHandler{
				Priority: 0,
				ID:       id,
				F: func(p pk.Packet) error {
					if p.Data == nil {
						return nil
					}
					return handler(p)
				},
			},
		)
	}

	client.Events.AddGeneric(bot.PacketHandler{Priority: 64, ID: 0, F: func(p pk.Packet) error {
		pid := p.ID
		if !ignorePid[pid] {
			value, _ := pidCount.LoadOrStore(pid, 0)
			count, _ := value.(int)
			pidCount.Store(pid, count+1)
		}

		return nil
	}})

	//Login
	err := client.JoinServer(*address)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Login success")

	//JoinGame
	err = client.HandleGame()
	if err != nil {
		log.Fatalf("game error: %v", err)
	}
}

func onDeath() error {
	log.Println("Died and Respawned")
	// If we exclude Respawn(...) then the player won't press the "Respawn" button upon death
	return player.Respawn()
}

func onGameStart() error {
	log.Println("Game start")

	watch = make(chan time.Time)
	go watchDog()

	go dumpInventoryPerSeconds(300)
	go dumpPacketCountPerSeconds(3)

	return UseItem(OffHand)

	// go walk()

	return nil
}

func onUpdatePosition(p pk.Packet) error {
	var (
		x, y, z    pk.Double
		yaw, pitch pk.Float
		flags      pk.Byte
		teleportID pk.VarInt
		dismount   pk.Boolean
	)

	p.Scan(&x, &y, &z, &yaw, &pitch, &flags, &teleportID, &dismount)
	myPosition.x = float64(x)
	myPosition.y = float64(y)
	myPosition.z = float64(z)

	log.Printf("玩家位置更新，我现在站在 %.1f/%.1f/%.1f flags=%#05b", x, y, z, flags)

	return nil
}

var seq int

func UseItem(hand int32) error {
	seq += 1
	return client.Conn.WritePacket(pk.Marshal(
		pktid.ServerboundUseItem,
		pk.VarInt(hand),
		pk.VarInt(seq),
	))
}

func Chat(format string, a ...interface{}) {
	client.Conn.WritePacket(pk.Marshal(
		pktid.ServerboundChat,
		pk.String(fmt.Sprintf(format, a...)),
	))
}

func onSpawnOrb(p pk.Packet) error {
	var (
		id      pk.VarInt
		x, y, z pk.Double
		count   pk.Short
	)

	p.Scan(&id, &x, &y, &z, &count)
	if math.Abs(float64(x)-myPosition.x)+math.Abs(float64(y)-myPosition.y)+math.Abs(float64(z)-myPosition.z) < 16 {
		log.Printf("发现经验球，包含经验值: %v", count)
	}

	return nil
}

func onGotExp(p pk.Packet) error {
	var (
		expBar   pk.Float
		level    pk.VarInt
		totalExp pk.VarInt
	)

	p.Scan(&expBar, &level, &totalExp)
	log.Printf("经验值更新: 总经验值 %v 人物等级 %v 升级进度条 %d%%", totalExp, level, int(expBar*100))

	return nil
}

func onEntitySpawning(p pk.Packet) error {
	var (
		id   pk.VarInt
		uuid pk.UUID
		typ  pk.VarInt

		x, y, z pk.Double

		pitch pk.Angle
		yaw   pk.Angle
		data  pk.Int
	)

	p.Scan(&id, &uuid, &typ, &x, &y, &z, &pitch, &yaw, &data)

	entity := entity.ByID[entity.ID(typ)]
	name := tryTrans("entity.minecraft." + entity.Name)
	if math.Abs(float64(x)-myPosition.x)+math.Abs(float64(y)-myPosition.y)+math.Abs(float64(z)-myPosition.z) < 32 {
		log.Printf("%v(%v:%v:%v)出现在 %.1f/%.1f/%.1f", name, typ, data, id, x, y, z)
	}

	return nil
}

const (
	Interact = 0
	Attack
	InteractAt
)

const (
	MainHand = 0
	OffHand  = 1
)

func dummyEventHandler(tag string) func(pk.Packet) error {
	return func(p pk.Packet) error {
		log.Printf("接收到数据包: %v(%v)", tag, p.ID)
		return nil
	}
}

/*
  "minecraft:block_entity_type": {
    "protocol_id": 10,
    "entries": {
      "minecraft:furnace": {
        "protocol_id": 0
      },
      "minecraft:chest": {
        "protocol_id": 1
      },
      "minecraft:trapped_chest": {
        "protocol_id": 2
      },
      "minecraft:ender_chest": {
        "protocol_id": 3
      },
      "minecraft:jukebox": {
        "protocol_id": 4
      },
      "minecraft:dispenser": {
        "protocol_id": 5
      },
      "minecraft:dropper": {
        "protocol_id": 6
      },
      "minecraft:sign": {
        "protocol_id": 7
      },
      "minecraft:mob_spawner": {
        "protocol_id": 8
      },
      "minecraft:piston": {
        "protocol_id": 9
      },
      "minecraft:brewing_stand": {
        "protocol_id": 10
      },
      "minecraft:enchanting_table": {
        "protocol_id": 11
      },
      "minecraft:end_portal": {
        "protocol_id": 12
      },
      "minecraft:beacon": {
        "protocol_id": 13
      },
      "minecraft:skull": {
        "protocol_id": 14
      },
      "minecraft:daylight_detector": {
        "protocol_id": 15
      },
      "minecraft:hopper": {
        "protocol_id": 16
      },
      "minecraft:comparator": {
        "protocol_id": 17
      },
      "minecraft:banner": {
        "protocol_id": 18
      },
      "minecraft:structure_block": {
        "protocol_id": 19
      },
      "minecraft:end_gateway": {
        "protocol_id": 20
      },
      "minecraft:command_block": {
        "protocol_id": 21
      },
      "minecraft:shulker_box": {
        "protocol_id": 22
      },
      "minecraft:bed": {
        "protocol_id": 23
      },
      "minecraft:conduit": {
        "protocol_id": 24
      },
      "minecraft:barrel": {
        "protocol_id": 25
      },
      "minecraft:smoker": {
        "protocol_id": 26
      },
      "minecraft:blast_furnace": {
        "protocol_id": 27
      },
      "minecraft:lectern": {
        "protocol_id": 28
      },
      "minecraft:bell": {
        "protocol_id": 29
      },
      "minecraft:jigsaw": {
        "protocol_id": 30
      },
      "minecraft:campfire": {
        "protocol_id": 31
      },
      "minecraft:beehive": {
        "protocol_id": 32
      },
      "minecraft:sculk_sensor": {
        "protocol_id": 33
      }
    }
  },
*/

type EntityMetadataEntry struct {
	Index pk.UnsignedByte
	Type  pk.VarInt
	Value pk.Field
}

func (em *EntityMetadataEntry) WriteTo(w io.Writer) (n int64, err error) {
	if em == nil {
		return pk.UnsignedByte(0xff).WriteTo(w)
	} else {
		return pk.Tuple{
			&em.Index, em.Type,
			// TODO: Value
		}.WriteTo(w)
	}
}

func (em *EntityMetadataEntry) ReadFrom(r io.Reader) (n int64, err error) {
	n, err = pk.Tuple{
		&em.Index, &em.Type,
	}.ReadFrom(r)

	// log.Printf("EntityMetadataEntry type: %v", em.Type)

	return
}

type EntityMetadata struct {
	entries []EntityMetadataEntry
}

func (em *EntityMetadata) WriteTo(w io.Writer) (n int64, err error) {
	return 0, nil
}

func (em *EntityMetadata) ReadFrom(r io.Reader) (n int64, err error) {
	var entry EntityMetadataEntry
	for {
		x, err := entry.ReadFrom(r)
		n += x
		if err != nil {
			return n, err
		}
	}

	return
}

func onBlockEntityData(p pk.Packet) error {
	var (
		location pk.Position
		typ      pk.VarInt
		data     nbt.RawMessage
	)

	if err := p.Scan(&location, &typ, pk.NBT(&data)); err != nil {
		log.Printf("onBlockEntityData 解析失败: %v", err)
	}

	log.Printf("onBlockEntityData: %v/%v/%v", location, typ, data.String())

	return nil
}

func onEntityData(p pk.Packet) error {
	var (
		id  pk.VarInt
		idx EntityMetadata
	)

	if err := p.Scan(&id, &idx); err != nil {
		return nil
	}

	// TODO:
	log.Printf("收到 %v 的详细数据。", id)

	return nil
}

func onEntityProperties(p pk.Packet) error {
	/*
		var (
			id    pk.VarInt
			count pk.VarInt
			key   pk.Identifier
			value pk.Double
			n     pk.VarInt
		)
	*/

	// p.Scan(&count, pk.Ary{&count, &ids})
	// log.Printf("onEntityProperties: %v", p)
	return nil
}

func onEntityRotate(p pk.Packet) error {
	return nil
}

func onSubtitle(p pk.Packet) error {
	var subtitle pk.Chat

	if err := p.Scan(&subtitle); err != nil {
		log.Printf("error: %v", err)
		return err
	}

	log.Printf("字幕: %s", subtitle)

	return nil
}

func onTakeItem(p pk.Packet) error {
	var (
		item      pk.VarInt
		collector pk.VarInt
	)

	if err := p.Scan(&item, &collector); err != nil {
		log.Printf("error: %v", err)
		return err
	}

	if int32(collector) == player.PlayerInfo.EID {
		log.Printf("我拾起了物品: %v", item)
	} else {
		// log.Printf("%v 拾起物品: %v", collector, item)
	}

	return nil
}

func GetSoundCategory(category pk.VarInt) string {
	categoryMap := map[pk.VarInt]string{
		0: "master",
		1: "music",
		2: "record",
		3: "weather",
		4: "block",
		5: "hostile",
		6: "neutral",
		7: "player",
		8: "ambient",
		9: "voice",
	}

	name, ok := categoryMap[category]
	if !ok {
		return "未知"
	}

	return tryTrans("soundCategory." + name)
}

//goland:noinspection SpellCheckingInspection
func onSound(p pk.Packet) error {
	var (
		soundID       pk.VarInt
		soundCategory pk.VarInt
		x, y, z       pk.Int
		volume, pitch pk.Float
	)
	if err := p.Scan(&soundID, &soundCategory, &x, &y, &z, &volume, &pitch); err != nil {
		log.Printf("音效解析错误: %v", err)
		return err
	}

	name, ok := soundid.GetSoundNameByID(soundid.SoundID(soundID))
	if name == "entity.generic.splash" { // 防止“溅起水花”刷屏
		return nil
	}

	msg := ""

	if ok {
		msg = tryTrans("subtitles." + name)
		log.Printf("%v：\x1b[36m%v\x1b[m", GetSoundCategory(soundCategory), msg)
		if msg == "流浪商人：喃喃自语" {
			Chat("流浪商人来了，快点！")
		} else if msg == "狐狸：吱吱叫" {
			Chat("狐狸来了，快点！")
		}
	}

	if msg == "浮漂：溅起水花" {
		log.Printf("鱼儿上钩了。")

		dumpPacketCount()

		if err := UseItem(OffHand); err != nil { // retrieve
			return err
		}
		time.Sleep(time.Millisecond * 500)
		if err := UseItem(OffHand); err != nil { // throw
			return err
		}
		watch <- time.Now()
	}

	return nil
}

func onUpdatePlayerInfo(p pk.Packet) error {
	var (
		action pk.VarInt
		number pk.VarInt
	)

	p.Scan(&action, &number)
	log.Printf("更新 %d 位玩家信息。action=%d", number, action)

	return nil
}

func onUpdateHealth(p pk.Packet) error {
	var (
		health         pk.Float
		food           pk.VarInt
		foodSaturation pk.Float
	)

	p.Scan(&health, &food, &foodSaturation)
	log.Printf("玩家血量更新: 血量=%.1f 食物=%v 饱食度=%.1f", health, food, foodSaturation)

	return nil
}

func onUpdateTime(p pk.Packet) error {
	var (
		worldAge  pk.Long
		timeOfDay pk.Long
	)

	p.Scan(&worldAge, &timeOfDay)
	time := int64(timeOfDay) % 24000
	if time%1000 == 0 {
		log.Printf("时间更新: %02d:%02d:**", (time/1000+6)%24, time%1000/60)
	}

	return nil
}

var myInventory [46]screen.Slot

func init() {
	zh_cn.Map["subtitles.entity.hostile.big_fall"] = "敌对生物：坠落"
	zh_cn.Map["subtitles.block.wood.step"] = "方块：走在木板上的脚步声"
	zh_cn.Map["subtitles.entity.zombie.step"] = "实体：僵尸的脚步声"
	zh_cn.Map["subtitles.entity.skeleton.step"] = "实体：骷髅的脚步声"
	zh_cn.Map["subtitles.entity.chicken.step"] = "实体：鸡的脚步声"
	zh_cn.Map["subtitles.entity.horse.land"] = "实体：马的脚步声"
}

func onScreenSlotChange(id, index int) error {
	if id == -2 {
		log.Printf("Slot: inventory: %v", screenManager.Inventory.Slots[index])
	} else if id == -1 && index == -1 {
		log.Printf("Slot: cursor: %v", screenManager.Cursor)
	} else {
		container, ok := screenManager.Screens[id]
		if ok {
			// Currently, only inventory container is supported
			switch container.(type) {
			case *screen.Inventory:
				slot := container.(*screen.Inventory).Slots[index]
				if myInventory[index].ID == slot.ID && myInventory[index].Count == slot.Count {
					return nil
				}

				myInventory[index] = slot

				itemInfo := item.ByID[item.ID(slot.ID)]
				if itemInfo == nil {
					return nil
				}

				// name, ok := zhName[itemInfo.DisplayName]
				name := tryTrans("item.minecraft."+itemInfo.Name, "block.minecraft."+itemInfo.Name)
				tag := &ItemTag{}
				slot.NBT.Unmarshal(&tag)
				if !tag.IsEmpty() {
					bytes, _ := json.Marshal(tag)
					json := strings.ReplaceAll(string(bytes), "ESC", "\x1b")
					log.Printf("获得了%v\x1b[m，属性为: %v", name, json)
				} else if itemInfo.StackSize > 1 {
					log.Printf("获得了%v\x1b[m，slot=%v(%v/%v)。", name, index, slot.Count, itemInfo.StackSize)
				} else {
					log.Printf("获得了%v\x1b[m, slot=%v。", name, index)
				}
			}
		}
	}

	return nil
}

func onKeepAlive(p pk.Packet) error {
	var id pk.Long
	p.Scan(&id)

	return client.Conn.WritePacket(pk.Marshal(
		pktid.ServerboundKeepAlive,
		pk.Long(id),
	))
}

func onUpdateRecipes(p pk.Packet) error {
	var n pk.VarInt
	p.Scan(&n)
	log.Printf("收到了 %v 个合成配方。", n)
	return nil
}

func onChatMsg(c *basic.PlayerMessage) error {
	log.Printf("\x1b[36m闲聊:%v\x1b[m", c)
	return nil
}

func onDisconnect(c chat.Message) error {
	log.Println("Disconnect:", c)
	return nil
}

func watchDog() {
	to := time.NewTimer(time.Second * timeout)
	for {
		select {
		case <-watch:
		case <-to.C:
			log.Println("rethrow")
			if err := UseItem(OffHand); err != nil {
				panic(err)
			}
		}
		to.Reset(time.Second * timeout)
	}
}

func dumpPacketCount() {
	count := ""
	pidCount.Range(func(key, value interface{}) bool {
		color := 32
		if value == 1 {
			color = 91
		}
		count = fmt.Sprintf("%s\x1b[%dm%#02x\x1b[m:\x1b[93m%d\x1b[m ", count, color, key, value)
		pidCount.Delete(key)
		return true
	})
	log.Printf("数据包计数器: %v", count)
}

func dumpPacketCountPerSeconds(seconds int) {
	for {
		time.Sleep(time.Second * time.Duration(seconds))
		dumpPacketCount()
	}
}

func dumpInventoryPerSeconds(seconds int) {
	for {
		time.Sleep(time.Second * time.Duration(seconds))
		dumpInventory()
	}
}

func dumpInventory() {
	log.Printf("我的背包内容:\n=======================")

	free := 0
	for i := 9; i <= 45; i++ {
		slot := myInventory[i]
		itemInfo := item.ByID[item.ID(slot.ID)]
		if itemInfo == nil || itemInfo == &item.Air {
			free++
			continue
		}

		name := tryTrans("item.minecraft."+itemInfo.Name, "block.minecraft."+itemInfo.Name)
		tag := &ItemTag{}
		slot.NBT.Unmarshal(&tag)
		if !tag.IsEmpty() {
			bytes, _ := json.Marshal(tag)
			json := strings.ReplaceAll(string(bytes), "ESC", "\x1b")
			log.Printf("[%02d] => %v\x1b[m，属性为: %v", i, name, json)
		} else if itemInfo.StackSize > 1 {
			log.Printf("[%02d] => %v\x1b[mx%v/%v。", i, name, slot.Count, itemInfo.StackSize)
		} else {
			log.Printf("[%02d] => %v\x1b[m。", i, name)
		}
	}

	log.Printf("此外还剩下 %v 个空闲的格子。\n=================", free)
}

func tryTrans(maybe ...string) string {
	var text string
	for _, text = range maybe {
		msg := chat.TranslateMsg(text).ClearString()
		if msg != "" {
			return msg
		}
	}

	return text
}
