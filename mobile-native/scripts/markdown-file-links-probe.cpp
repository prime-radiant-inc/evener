// Compile against the installed package's unchanged parser and MD4C sources.
#include "MD4CParser.hpp"
#include <iostream>
#include <iterator>

static std::string labelText(const std::shared_ptr<Markdown::MarkdownASTNode>& node) {
    std::string text = node->content;
    for (const auto& child : node->children) text += labelText(child);
    return text;
}

static void emitHex(const std::string& text) {
    constexpr char hex[] = "0123456789abcdef";
    for (const unsigned char byte : text) {
        std::cout << hex[byte >> 4] << hex[byte & 15];
    }
}

static void emitLinks(const std::shared_ptr<Markdown::MarkdownASTNode>& node) {
    if (node->type == Markdown::NodeType::Link) {
        const auto url = node->attributes.find("url");
        if (url != node->attributes.end()) {
            emitHex(url->second);
            std::cout << '\t';
            emitHex(labelText(node));
            std::cout << '\n';
        }
    }
    for (const auto& child : node->children) emitLinks(child);
}

int main() {
    const std::string input(std::istreambuf_iterator<char>(std::cin), {});
    Markdown::MD4CParser parser;
    emitLinks(parser.parse(input));
    return std::cout.good() ? 0 : 1;
}
