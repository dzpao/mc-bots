package main

import (
	"fmt"
	"strings"
)

type Position struct {
	x, y, z float64
}

type ItemTag struct {
	Damage             int          `nbt:"Damage" json:"受损,omitempty"`
	StoredEnchantments Enchantments `nbt:"StoredEnchantments" json:"内含魔咒,omitempty"`
	Enchantments       Enchantments `nbt:"Enchantments" json:"已附魔,omitempty"`
	RepairCost         int          `nbt:"RepairCost" json:"铁砧惩罚,omitempty"`
	Potion             Potion       `nbt:"Potion,omitempty" json:"药水,omitempty"`
	Text               string
}

func (tag ItemTag) IsEmpty() bool {
	return tag.StoredEnchantments == nil &&
		tag.Enchantments == nil &&
		tag.RepairCost == 0 &&
		tag.Potion == "" &&
		tag.Damage == 0
}

type Potion string

func (p Potion) String() string {
	return tryTrans(strings.ReplaceAll(string(p), "minecraft:", "item.minecraft.potion.effect."))
}

func (p Potion) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%s"`, p.String())), nil
}

type Enchantment struct {
	ID    string `nbt:"id"`
	Level int    `nbt:"lvl"`
}

func (e Enchantment) String() string {
	zhCN := tryTrans("enchantment." + strings.ReplaceAll(e.ID, ":", "."))

	if strings.Contains("引雷/忠诚/抢夺/精准采集/时运/荆棘/经验修补", zhCN) {
		zhCN = fmt.Sprintf("ESC[4;1;92m%sESC[m", zhCN)
	}

	return fmt.Sprintf("%s%d", zhCN, e.Level)
}

func (e Enchantment) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%s%d"`, e.String())), nil
}

type Enchantments []Enchantment

func (es Enchantments) MarshalJSON() ([]byte, error) {
	var parts []string
	for _, e := range es {
		o := e.String()
		parts = append(parts, o)
	}

	return []byte(fmt.Sprintf(`"%s"`, strings.Join(parts, "/"))), nil
}
