package ask

import "testing"

func TestCaptionLexicalScorePrefersConcreteChineseSubject(t *testing.T) {
	question := "我有和金毛犬相关的图片吗？图片里是什么场景？"
	golden := "一只金毛犬站立在落叶覆盖的地面上，背景是树林。"
	cat := "一只橙色虎斑猫正坐着看镜头。"

	if captionLexicalScore(question, golden) <= captionLexicalScore(question, cat) {
		t.Fatalf("expected golden-retriever caption to outrank cat caption")
	}
}

func TestCaptionLexicalScoreHandlesSingleCharacterObject(t *testing.T) {
	question := "我有猫的照片吗？"
	cat := "一只橙色虎斑猫正坐着看镜头。"
	dog := "一只金毛犬站立在落叶覆盖的地面上。"

	if captionLexicalScore(question, cat) <= captionLexicalScore(question, dog) {
		t.Fatalf("expected cat caption to outrank dog caption")
	}
}

func TestCaptionLexicalScoreIgnoresGenericImageWords(t *testing.T) {
	question := "这张图片是什么场景？"
	if got := captionLexicalScore(question, "图片内容"); got != 0 {
		t.Fatalf("expected generic image words to add no score, got %v", got)
	}
}
