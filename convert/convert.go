package convert

import (
	"encoding/json"
	"github.com/env0/terratag/errors"
	"github.com/env0/terratag/tag_keys"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/thoas/go-funk"
	"github.com/zclconf/go-cty/cty"
	"log"
	"strings"
)

func GetExistingTagsExpression(tokens hclwrite.Tokens) string {
	if isHclMap(tokens) {
		return buildMapExpression(tokens)
	} else {
		return stringifyExpression(tokens)
	}
}

func isHclMap(tokens hclwrite.Tokens) bool {
	maybeHclMap := strings.TrimSpace(string(tokens.Bytes()))
	return strings.HasPrefix(maybeHclMap, "{") && strings.HasSuffix(maybeHclMap, "}")
}

func buildMapExpression(tokens hclwrite.Tokens) string {
	// Need to convert to inline map expression

	// First, we remove the first and last two tokens - get rid of the openning { closing } and newline
	tokens = tokens[1:]
	tokens = tokens[:len(tokens)-2]

	// Then, we normalize the key-value paris so that they would be seperated by comma
	// That's cause HCL supports both newline, comma or a combination of the two in seperating values
	// This will make it easier to split the key-value pairs later
	var tokensToRemove hclwrite.Tokens
	for i, token := range tokens {
		if token.Type == hclsyntax.TokenNewline {
			// make sure there is (or was) no comma before
			if i > 0 && tokens[i-1].Type != hclsyntax.TokenComma && !funk.Contains(tokensToRemove, tokens[i-1]) {
				tokens[i] = &hclwrite.Token{
					Type:         hclsyntax.TokenComma,
					Bytes:        []byte(","),
					SpacesBefore: 1,
				}
			} else { // if there is, we should remove this new line, so we'll only have the comma
				tokensToRemove = append(tokensToRemove, token)
			}
		}
	}

	// Remove tall the new lines we marked for removal
	for _, tokenToRemove := range tokensToRemove {
		indexToRemove := funk.IndexOf(tokens, tokenToRemove)
		tokens = append(tokens[:indexToRemove], tokens[indexToRemove+1:]...)
	}

	// At this point there should be no new lines, only a single comma seperating between key values
	// Since the map() gets a flat set of pairs as map("key1","value1","key2","value2"),
	// we can just replace any assignment operator (=) with comma
	for i, token := range tokens {
		if token.Type == hclsyntax.TokenEqual {
			tokens[i] = &hclwrite.Token{
				Type:         hclsyntax.TokenComma,
				Bytes:        []byte(","),
				SpacesBefore: token.SpacesBefore,
			}
		}
	}

	mapContent := string(tokens.Bytes())
	mapContent = strings.TrimSpace(mapContent)
	mapContent = strings.TrimSuffix(mapContent, ",") // remove any traling commas due to newline replaced
	return "map(" + mapContent + ")"
}

func stringifyExpression(tokens hclwrite.Tokens) string {
	expression := strings.TrimSpace(string(tokens.Bytes()))
	// may be wrapped with ${ } in TF11
	expression = strings.TrimPrefix(expression, "${")
	expression = strings.TrimSuffix(expression, "${")

	return expression
}

func AppendLocalsBlock(file *hclwrite.File, filename string, terratag TerratagLocal) {
	file.Body().AppendNewline()
	locals := file.Body().AppendNewBlock("locals", nil)
	file.Body().AppendNewline()

	locals.Body().SetAttributeValue(tag_keys.GetTerratagAddedKey(filename), cty.StringVal(terratag.Added))
}

func AppendTagBlocks(resource *hclwrite.Block, tags string) {
	var tagsMap map[string]string
	err := json.Unmarshal([]byte(tags), &tagsMap)
	errors.PanicOnError(err, nil)

	for key, value := range tagsMap {
		resource.Body().AppendNewline()
		tagBlock := resource.Body().AppendNewBlock("tag", nil)
		tagBlock.Body().SetAttributeValue("key", cty.StringVal(key))
		tagBlock.Body().SetAttributeValue("value", cty.StringVal(value))
		tagBlock.Body().SetAttributeValue("propagate_at_launch", cty.BoolVal(true))
	}
}

func UnquoteTagsAttribute(swappedTagsStrings []string, text string) string {
	for _, swappedTagString := range swappedTagsStrings {
		escapedByWriter := strings.ReplaceAll(swappedTagString, "\"", "\\\"")

		if strings.HasPrefix(swappedTagString, "${") && strings.HasSuffix(swappedTagString, "}") {
			escapedByWriter = strings.ReplaceAll(escapedByWriter, "${", "$${")
		} else {
			escapedByWriter = "\"" + escapedByWriter + "\""
		}

		text = strings.ReplaceAll(text, escapedByWriter, swappedTagString)
	}
	return text
}

func MoveExistingTags(filename string, terratag TerratagLocal, resource *hclwrite.Block) bool {
	var existingTags hclwrite.Tokens

	// First we try to find tags as attribute
	tagsAttribute := resource.Body().GetAttribute("tags")

	if tagsAttribute != nil {
		// If attribute found, get its value
		log.Print("Preexisting tags ATTRIBUTE found on resource. Merging.")
		existingTags = tagsAttribute.Expr().BuildTokens(hclwrite.Tokens{})
	} else {
		// Otherwise, we try to get tags as block
		tagsBlock := resource.Body().FirstMatchingBlock("tags", nil)
		if tagsBlock != nil {
			quaotedTagBlock := quoteBlockKeys(tagsBlock)
			existingTags = funk.Tail(quaotedTagBlock.BuildTokens(hclwrite.Tokens{})).(hclwrite.Tokens)
			// If we did get tags from block, we will now remove that block, as we're going to add a merged tags ATTRIBUTE
			removeBlockResult := resource.Body().RemoveBlock(tagsBlock)
			if removeBlockResult == false {
				log.Fatal("Failed to remove found tags block!")
			}
		}
	}

	if existingTags != nil {
		terratag.Found[tag_keys.GetResourceExistingTagsKey(filename, resource)] = existingTags
		return true
	}
	return false
}

func quoteBlockKeys(tagsBlock *hclwrite.Block) *hclwrite.Block {
	// In HCL, block keys must NOT be quoted
	// But we need them to be, as we throw them into a map() function as strings
	quaotedTagBlock := hclwrite.NewBlock(tagsBlock.Type(), tagsBlock.Labels())
	for key, value := range tagsBlock.Body().Attributes() {
		quaotedTagBlock.Body().SetAttributeRaw("\""+key+"\"", value.Expr().BuildTokens(hclwrite.Tokens{}))
	}
	return quaotedTagBlock
}

type TerratagLocal struct {
	Found map[string]hclwrite.Tokens
	Added string
}