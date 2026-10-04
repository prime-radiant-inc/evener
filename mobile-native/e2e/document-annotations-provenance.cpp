// Compile against the installed vendored parser, not a source-map reimplementation.
#include "md4c.h"
#include "MD4CParser.hpp"
#include <cstdlib>
#include <iostream>
#include <string>
#include <vector>

struct Fragment {
  MD_TEXTTYPE type;
  std::string text;
  unsigned start;
  unsigned end;
  bool generated;
};
using Fragments = std::vector<Fragment>;
static int block(MD_BLOCKTYPE, void *, void *) { return 0; }
static int span(MD_SPANTYPE, void *, void *) { return 0; }
static int text(MD_TEXTTYPE type, const MD_CHAR *value, MD_SIZE size, void *data) {
  static_cast<Fragments *>(data)->push_back({type, std::string(value, size), 0, 0, true});
  return 0;
}
#ifdef MD4C_SOURCE_SPANS
static int mappedText(MD_TEXTTYPE type, const MD_CHAR *value, MD_SIZE size,
                      MD_OFFSET start, MD_OFFSET end, int generated, void *data) {
  static_cast<Fragments *>(data)->push_back({type, std::string(value, size), start, end, generated != 0});
  return 0;
}
#endif
static void check(const char *name, const std::string &source, const Fragments &expected,
                  unsigned flags = MD_FLAG_NOHTML | MD_FLAG_TABLES) {
  Fragments actual;
  MD_PARSER parser = {};
  parser.flags = flags;
  parser.enter_block = parser.leave_block = block;
  parser.enter_span = parser.leave_span = span;
  parser.text = text;
#ifdef MD4C_SOURCE_SPANS
  parser.text_with_source = mappedText;
#endif
  if (md_parse(source.data(), static_cast<MD_SIZE>(source.size()), &parser, &actual) != 0) {
    std::cerr << name << ": parse failed\n";
    std::exit(1);
  }
  bool equal = actual.size() == expected.size();
  for (size_t i = 0; equal && i < actual.size(); ++i) {
    const auto &a = actual[i];
    const auto &e = expected[i];
    equal = a.type == e.type && a.text == e.text && a.start == e.start && a.end == e.end && a.generated == e.generated;
  }
  if (!equal) {
    std::cerr << name << ": incorrect original byte fragments\n";
    for (const auto &a : actual)
      std::cerr << "  type=" << a.type << " text=" << a.text << " [" << a.start << "," << a.end << ") generated=" << a.generated << "\n";
    std::exit(1);
  }
  // Optional provenance must not change the ordinary callback's rendered bytes.
  Fragments legacy;
#ifdef MD4C_SOURCE_SPANS
  parser.text_with_source = nullptr;
#endif
  if (md_parse(source.data(), static_cast<MD_SIZE>(source.size()), &parser, &legacy) != 0 || legacy.size() != actual.size())
    std::exit(1);
  for (size_t i = 0; i < actual.size(); ++i)
    if (legacy[i].text != actual[i].text || legacy[i].type != actual[i].type) std::exit(1);
  std::cout << "PASS " << name << "\n";
}
static void collect(const Markdown::MarkdownASTNode &node, std::vector<Markdown::SourceFragment> &fragments) {
  fragments.insert(fragments.end(), node.sourceFragments.begin(), node.sourceFragments.end());
  for (const auto &child : node.children) collect(*child, fragments);
}
static void checkAST(const char *name, const std::string &source,
                     const std::vector<Markdown::SourceFragment> &expected) {
  Markdown::MD4CParser parser;
  const auto ast = parser.parse(source);
  std::vector<Markdown::SourceFragment> actual;
  collect(*ast, actual);
  bool equal = actual.size() == expected.size();
  for (size_t i = 0; equal && i < actual.size(); ++i) {
    const auto &a = actual[i];
    const auto &e = expected[i];
    equal = a.text == e.text && a.sourceByteStart == e.sourceByteStart &&
            a.sourceByteEnd == e.sourceByteEnd && a.generated == e.generated;
  }
  if (!equal) {
    std::cerr << name << ": incorrect AST fragments\n";
    for (const auto &a : actual)
      std::cerr << "  text=" << a.text << " [" << a.sourceByteStart << "," << a.sourceByteEnd << ") generated=" << a.generated << "\n";
    std::exit(1);
  }
  std::cout << "PASS " << name << "\n";
}
static void checkCodeBounds(const char *name, const std::string &source, size_t start, size_t end) {
  Markdown::MD4CParser parser;
  auto ast = parser.parse(source);
  if (ast->children.size() != 1 || ast->children.front()->type != Markdown::NodeType::CodeBlock)
    std::exit(1);
  const auto &node = *ast->children.front();
  if (!node.hasSourceRange || node.sourceByteStart != start || node.sourceByteEnd != end) {
    std::cerr << name << ": incorrect code-block span [" << node.sourceByteStart << "," << node.sourceByteEnd << ")\n";
    std::exit(1);
  }
  std::cout << "PASS " << name << "\n";
}
static void checkBlockBounds(const char *name, const std::string &source, Markdown::NodeType type,
                             size_t start, size_t end) {
  Markdown::MD4CParser parser;
  auto ast = parser.parse(source);
  if (ast->children.size() != 1 || ast->children.front()->type != type) std::exit(1);
  const auto &node = *ast->children.front();
  if (!node.hasSourceRange || node.sourceByteStart != start || node.sourceByteEnd != end) {
    std::cerr << name << ": incorrect full block span [" << node.sourceByteStart << "," << node.sourceByteEnd << ")\n";
    std::exit(1);
  }
  std::cout << "PASS " << name << "\n";
}
int main() {
  check("repeated strong text", "same **same**", {
    {MD_TEXT_NORMAL, "same ", 0, 5, false}, {MD_TEXT_NORMAL, "same", 7, 11, false}
  });
  check("entity and escape", "&amp; \\*", {
    {MD_TEXT_ENTITY, "&amp;", 0, 5, false}, {MD_TEXT_NORMAL, " ", 5, 6, false},
    {MD_TEXT_NORMAL, "*", 6, 8, false}
  });
  check("CRLF soft break", "first\r\nsecond", {
    {MD_TEXT_NORMAL, "first", 0, 5, false}, {MD_TEXT_SOFTBR, "\n", 5, 7, false},
    {MD_TEXT_NORMAL, "second", 7, 13, false}
  });
  check("inline code CRLF normalization", "`same\r\nnext`", {
    {MD_TEXT_CODE, "same", 1, 5, false}, {MD_TEXT_CODE, " ", 5, 7, false},
    {MD_TEXT_CODE, "next", 7, 11, false}
  });
  check("fenced code trailing CRLF", "```ts\r\nsame()\r\n\r\n```\r\n", {
    {MD_TEXT_CODE, "same()", 7, 13, false}, {MD_TEXT_CODE, "\n", 13, 15, false},
    {MD_TEXT_CODE, "\n", 15, 17, false}
  });
  check("code EOF generated newline", "```\nsame()", {
    {MD_TEXT_CODE, "same()", 4, 10, false}, {MD_TEXT_CODE, "\n", 10, 10, true}
  });
  check("tab-expanded indented code", "    \tcode\r\n", {
    {MD_TEXT_CODE, "    ", 4, 5, false}, {MD_TEXT_CODE, "code", 5, 9, false},
    {MD_TEXT_CODE, "\n", 9, 11, false}
  });
  check("escaped hard break", "a\\\r\nb", {
    {MD_TEXT_NORMAL, "a", 0, 1, false}, {MD_TEXT_BR, "\n", 1, 4, false},
    {MD_TEXT_NORMAL, "b", 4, 5, false}
  });
  check("embedded NUL in code", std::string("```\na\0b\n```\n", 12), {
    {MD_TEXT_CODE, "a", 4, 5, false}, {MD_TEXT_NULLCHAR, std::string("\0", 1), 5, 6, false},
    {MD_TEXT_CODE, "b", 6, 7, false}, {MD_TEXT_CODE, "\n", 7, 8, false}
  });
  check("collapsed whitespace original span", "a  \tb", {
    {MD_TEXT_NORMAL, "a", 0, 1, false}, {MD_TEXT_NORMAL, " ", 1, 4, false},
    {MD_TEXT_NORMAL, "b", 4, 5, false}
  }, MD_FLAG_COLLAPSEWHITESPACE);
  check("two-space hard break", "a  \r\nb", {
    {MD_TEXT_NORMAL, "a", 0, 1, false}, {MD_TEXT_BR, "\n", 1, 5, false},
    {MD_TEXT_NORMAL, "b", 5, 6, false}
  });
  checkAST("AST astral prefix original bytes", "😀 **same**", {
    {"😀 ", 0, 5, false}, {"same", 7, 11, false}
  });
  checkAST("AST original BOM retained", "\xEF\xBB\xBF**same**", {
    {"\xEF\xBB\xBF", 0, 3, false}, {"same", 5, 9, false}
  });
  checkAST("AST original reference definition", "same [same][r]\n\n[r]: /ok\n", {
    {"same ", 0, 5, false}, {"same", 6, 10, false}
  });
  checkAST("AST escaped table pipe", "| A |\n| --- |\n| same\\|tail |\n", {
    {"A", 2, 3, false}, {"same", 16, 20, false}, {"|", 20, 22, false}, {"tail", 22, 26, false}
  });
  checkAST("AST tab after quote marker", ">     \tcode\r\n", {
    {"  ", 6, 7, false}, {"code", 7, 11, false}, {"\n", 11, 13, false}
  });
  checkAST("AST tab after list marker", "-     \tcode\n", {
    {"  ", 6, 7, false}, {"code", 7, 11, false}, {"\n", 11, 12, false}
  });
  checkAST("AST repeated text across blocks", "**same**\r\n\r\nsame", {
    {"same", 2, 6, false}, {"same", 12, 16, false}
  });
  checkAST("AST full entity decoding", "&amp;&NotEqualTilde;&#x1F600;&#128;&#0;&unknown;", {
    {"&", 0, 5, false}, {"≂̸", 5, 20, false}, {"😀", 20, 29, false},
    {"€", 29, 35, false}, {"�", 35, 39, false}, {"&unknown;", 39, 48, false}
  });
  checkAST("AST code CRLF and blanks", "```\r\na\r\n\r\n```\r\n", {
    {"a", 5, 6, false}, {"\n", 6, 8, false}, {"\n", 8, 10, false}
  });
  checkAST("AST NUL replacement", std::string("```\na\0b\n```\n", 12), {
    {"a", 4, 5, false}, {"�", 5, 6, false}, {"b", 6, 7, false}, {"\n", 7, 8, false}
  });
  checkCodeBounds("empty fenced block", "```\n```\n", 0, 8);
  checkCodeBounds("Reader empty literal block", "```text\n\n```\n", 0, 13);
  checkCodeBounds("indented block includes original tab", "    \tcode\r\n", 0, 11);
  checkCodeBounds("unterminated block excludes generated LF", "```text\nsame()", 0, 14);
  checkBlockBounds("full heading syntax and CRLF", "# Note\r\n", Markdown::NodeType::Heading, 0, 8);
  checkBlockBounds("full paragraph syntax and CRLF", "**same**\r\n", Markdown::NodeType::Paragraph, 0, 10);
  checkBlockBounds("empty heading original span", "#\r\n", Markdown::NodeType::Heading, 0, 3);
  checkBlockBounds("thematic rule original span", "---\r\n", Markdown::NodeType::ThematicBreak, 0, 5);
  checkBlockBounds("full table syntax and CRLF", "| A |\r\n| --- |\r\n| same |\r\n", Markdown::NodeType::Table, 0, 26);
}
