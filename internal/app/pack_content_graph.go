package app

import (
 "encoding/json"
 "fmt"
 "io/fs"
 "os"
 "path/filepath"
 "sort"
 "strconv"
 "strings"
)

// Content identities describe game objects; locations are version-specific.
// The same identity can occur in several files and subpacks.
type contentLocation struct { File string; Pointer []string }
type contentNode struct {
 Refs []string `json:"refs,omitempty"`
 Locations []contentLocation `json:"-"`
 Strings []string `json:"-"`
}
type contentGraph map[string]*contentNode
func contentID(kind, id string) string { return kind+"|"+id }
func gameID(id string) string { if !strings.Contains(id, ":") { return "minecraft:"+id }; return id }
func (g contentGraph) add(id string, loc *contentLocation, refs ...string) *contentNode {
 n:=g[id]; if n==nil { n=&contentNode{}; g[id]=n }; n.Refs=append(n.Refs,refs...)
 if loc!=nil { n.Locations=append(n.Locations,*loc) }; return n
}
func contentStrings(v any) []string {
 switch v:=v.(type) { case string: return []string{v}; case []any: var out []string; for _,x:=range v { out=append(out,contentStrings(x)...)}; return out; case map[string]any: var out []string; for k,x:=range v { if k!="identifier" {out=append(out,contentStrings(x)...)} }; return out }; return nil
}
func object(v any) map[string]any { m,_:=v.(map[string]any); return m }
func nested(v any, path ...string) any { for _,p:=range path { v=object(v)[p] }; return v }
func textValue(v any) string { s,_:=v.(string); return s }
func logicalContentPath(path string) string {
 parts:=strings.Split(path,"/"); if len(parts)>2 && parts[0]=="subpacks" { return strings.Join(parts[2:],"/") }; return path
}
func contentFiles(root string) ([]string,error) {
 var files []string
 err:=filepath.WalkDir(root,func(path string,e fs.DirEntry,err error) error {
  if err!=nil{return err}; if e.Type()&os.ModeSymlink!=0{return fmt.Errorf("Symlink in pack: %s",path)}
  if e.IsDir(){return nil}; if !e.Type().IsRegular(){return fmt.Errorf("Non-regular pack file: %s",path)}
  rel,err:=filepath.Rel(root,path); if err!=nil{return err}; if !assetRelative(filepath.ToSlash(rel)){return fmt.Errorf("Unsafe pack path")}; files=append(files,filepath.ToSlash(rel)); return nil
 }); sort.Strings(files); return files,err
}
func scanContent(root string) (contentGraph,[]string,error) {
 g:=contentGraph{}; warnings:=[]string{}; files,err:=contentFiles(root); if err!=nil{return nil,nil,err}
 var jsonBytes int64
 for _,file:=range files {
  logical:=logicalContentPath(file)
  g.add(contentID("file",logical),&contentLocation{File:file})
  if !strings.HasSuffix(strings.ToLower(file),".json") || logical=="manifest.json" {continue}
  info,err:=os.Stat(filepath.Join(root,filepath.FromSlash(file))); if err!=nil{return nil,nil,err}; jsonBytes+=info.Size()
  if info.Size()>16<<20 || jsonBytes>128<<20{return nil,nil,fmt.Errorf("Pack JSON exceeds content analysis limit")}
  data,err:=os.ReadFile(filepath.Join(root,filepath.FromSlash(file))); if err!=nil{return nil,nil,err}
  var doc any; if err:=json.Unmarshal(stripJSONComments(data),&doc); err!=nil {warnings=append(warnings,"Could not inspect "+file); continue}
  add:=func(kind,id string,pointer []string,value any) *contentNode {n:=g.add(contentID(kind,id),&contentLocation{File:file,Pointer:pointer}); n.Strings=append(n.Strings,contentStrings(value)...); return n}
  catalog:=func(kind string,path ...string){ for id,v:=range object(nested(doc,path...)) { if id=="format_version" {continue}; add(kind,id,append(append([]string{},path...),id),v) } }
  switch logical {
  case "blocks.json":
   for id,v:=range object(doc) { if id=="format_version" {continue}; n:=add("block",gameID(id),[]string{id},v)
    for _,field:=range []string{"textures","carried_textures"} {for _,alias:=range contentStrings(object(v)[field]) {n.Refs=append(n.Refs,contentID("terrain",alias))}}
    if sound:=textValue(object(v)["sound"]);sound!="" {n.Refs=append(n.Refs,contentID("blockSound",sound))}
   }
  case "textures/terrain_texture.json": catalog("terrain","texture_data")
  case "textures/item_texture.json": catalog("itemTexture","texture_data")
  case "sounds/sound_definitions.json":
   if object(doc)["sound_definitions"]!=nil {catalog("sound","sound_definitions")} else {catalog("sound")}
  case "sounds.json":
   for _,prefix:=range [][]string{{},{"interactive_sounds"}} {
    for id,v:=range object(nested(doc,append(append([]string{},prefix...),"block_sounds")...)) {add("blockSound",id,append(append([]string{},prefix...),"block_sounds",id),v)}
    for id,v:=range object(nested(doc,append(append([]string{},prefix...),"entity_sounds","entities")...)) {add("entity",gameID(id),append(append([]string{},prefix...),"entity_sounds","entities",id),v)}
   }
   catalog("event","individual_event_sounds","events")
  case "music_definitions.json": catalog("music")
  case "textures/flipbook_textures.json", "textures/flipbook_texture.json":
   if list,ok:=doc.([]any);ok { for i,v:=range list {if id:=textValue(object(v)["atlas_tile"]);id!="" {add("terrain",id,[]string{strconv.Itoa(i)},v)}} }
  default:
   for _,spec:=range []struct{key,kind string}{{"minecraft:client_entity","entity"},{"minecraft:attachable","attachable"},{"minecraft:item","item"},{"particle_effect","particle"},{"minecraft:fog_settings","fog"}} {
    v:=object(doc)[spec.key]; id:=textValue(nested(v,"description","identifier")); if id!="" {n:=add(spec.kind,gameID(id),nil,v); if spec.kind=="item" {for _,alias:=range contentStrings(nested(v,"components","minecraft:icon")){n.Refs=append(n.Refs,contentID("itemTexture",alias))}} }
   }
   for key,kind:=range map[string]string{"animations":"animation","animation_controllers":"animationController","render_controllers":"renderController","materials":"material"} {if object(doc)[key]!=nil {catalog(kind,key)}}
   if list,ok:=object(doc)["minecraft:geometry"].([]any);ok {for i,v:=range list {if id:=textValue(nested(v,"description","identifier"));id!="" {add("geometry",id,[]string{"minecraft:geometry",strconv.Itoa(i)},v)}}}
   // Old geometry files use their identifier as a top-level key.
   for id,v:=range object(doc) {if strings.HasPrefix(id,"geometry.") {add("geometry",id,[]string{id},v)}}
  }
 }
 return g,warnings,nil
}
func resolveContentRefs(g contentGraph, reference contentGraph) {
 identifiers:=map[string][]string{}
 for _,graph:=range []contentGraph{reference,g} {for id:=range graph {kind,name,_:=strings.Cut(id,"|"); if kind!="file" && kind!="block" && kind!="entity" && kind!="item" {identifiers[name]=append(identifiers[name],id)}}}
 for id,n:=range g {
  for _,s:=range n.Strings {
   n.Refs=append(n.Refs,identifiers[s]...)
   if strings.HasPrefix(s,"textures/") {for _,ext:=range []string{"",".png",".tga",".jpg",".jpeg"} {n.Refs=append(n.Refs,contentID("file",s+ext))}}
   if strings.HasPrefix(s,"sounds/") {for _,ext:=range []string{"",".ogg",".wav",".fsb"} {n.Refs=append(n.Refs,contentID("file",s+ext))}}
  }
  n.Refs=uniqueContentStrings(n.Refs); out:=n.Refs[:0];for _,ref:=range n.Refs {if ref!=id{out=append(out,ref)}}; n.Refs=out
 }
}
func uniqueContentStrings(values []string) []string {set:=map[string]bool{};for _,v:=range values {set[v]=true}; out:=make([]string,0,len(set));for v:=range set {out=append(out,v)};sort.Strings(out);return out}
func contentClosure(g contentGraph,id string) map[string]bool {out:=map[string]bool{};var visit func(string);visit=func(id string){if out[id]{return};out[id]=true;if n:=g[id];n!=nil {for _,ref:=range n.Refs {visit(ref)}}};visit(id);return out}
func contentCategory(id string) string {kind,_,_:=strings.Cut(id,"|");switch kind {case "block":return "Blocks";case "entity":return "Mobs";case "item","attachable":return "Items";case "sound","blockSound","event":return "Sounds";case "music":return "Music";case "particle":return "Particles";case "fog":return "Fogs"};return ""}
