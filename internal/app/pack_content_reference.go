package app

import (
 _ "embed"
 "encoding/json"
 "os"
 "path/filepath"
 "strings"
)

// Only identifiers and dependency paths are embedded, never Mojang art or audio.
//go:embed content_reference/vanilla.json
var vanillaContentData []byte

func vanillaContentGraph(data string) (contentGraph,error) {
 refs:=map[string][]string{};if err:=json.Unmarshal(vanillaContentData,&refs);err!=nil{return nil,err}
 graph:=contentGraph{};for id,dependencies:=range refs {graph[id]=&contentNode{Refs:dependencies}}
 // Include definitions shipped with this server, including versioned vanilla packs.
 entries,err:=os.ReadDir(filepath.Join(data,"resource_packs"));if err!=nil{return nil,err}
 for _,entry:=range entries {if !entry.IsDir() || !(entry.Name()=="vanilla" || entry.Name()=="vanilla_base" || strings.HasPrefix(entry.Name(),"vanilla_")){continue}
  extra,_,err:=scanContent(filepath.Join(data,"resource_packs",entry.Name()));if err!=nil{return nil,err};resolveContentRefs(extra,graph)
  for id,n:=range extra {graph.add(id,nil,n.Refs...)}
 }
 return graph,nil
}
