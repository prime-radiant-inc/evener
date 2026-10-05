// Native Foundation regression, built against the real patched parser/model.
#import <Foundation/Foundation.h>
#import "ENRMOriginalDocumentModel.h"

static void check(BOOL value, const char *name)
{
  if (!value) { fprintf(stderr, "FAIL %s\n", name); exit(1); }
  printf("PASS %s\n", name);
}
static void checkRange(NSRange actual, NSUInteger start, NSUInteger end, const char *name)
{
  if (actual.location != start || actual.length != end - start) {
    fprintf(stderr, "FAIL %s, got [%lu,%lu), expected [%lu,%lu)\n", name,
            (unsigned long)actual.location, (unsigned long)NSMaxRange(actual),
            (unsigned long)start, (unsigned long)end);
    exit(1);
  }
  printf("PASS %s\n", name);
}
static NSMutableAttributedString *renderFragments(NSArray<ENRMSourceFragment *> *fragments)
{
  NSMutableAttributedString *result = [[NSMutableAttributedString alloc] initWithString:@""];
  for (ENRMSourceFragment *fragment in fragments)
    [result appendAttributedString:[[NSAttributedString alloc] initWithString:fragment.text
           attributes:@{ ENRMSourceFragmentAttributeName: fragment }]];
  return result;
}
static NSMutableAttributedString *renderNode(MarkdownASTNode *node)
{
  NSMutableAttributedString *result = renderFragments(node.sourceFragments);
  if (!node.sourceFragments.count)
    for (MarkdownASTNode *child in node.children) [result appendAttributedString:renderNode(child)];
  return result;
}
static void code(NSString *source, NSString *literal, NSUInteger blockEnd, NSUInteger textEnd, const char *name)
{
  ENRMOriginalDocumentModel *model = [[ENRMOriginalDocumentModel alloc] initWithSource:source snapshotId:@"code"];
  NSArray *fragments = [model codeFragmentsForText:literal blockRange:NSMakeRange(0, blockEnd)];
  check(fragments != nil, name);
  NSAttributedString *rendered = renderFragments(fragments);
  check([rendered.string isEqualToString:literal], "exact supplied code text");
  if (literal.length) checkRange([model sourceRangeForRenderedRange:NSMakeRange(0, literal.length) inText:rendered],
                                8, textEnd, "independent original full code body span");
  check([model codeFragmentsForText:[literal stringByAppendingString:@"WRONG"] blockRange:NSMakeRange(0, blockEnd)] == nil,
        "mismatching code input has no map");
}
int main(void)
{
  @autoreleasepool {
    NSString *rich = @"# Note\r\n\r\nfirst **same** [link](https://example.test) &amp; 😀\r\n\r\n"
                      "second same\r\n\r\n- item **same**\r\n\r\n"
                      "| Cell | Other |\r\n| --- | --- |\r\n| same | tail |\r\n\r\n```ts\r\nsame()\r\n```\r\n";
    ENRMOriginalDocumentModel *model = [[ENRMOriginalDocumentModel alloc] initWithSource:rich snapshotId:@"rich"];
    check(model != nil && [model.source isEqualToString:rich] && [model.displayedSnapshotId isEqualToString:@"rich"],
          "immutable original CRLF source and snapshot identity");
    NSString *mapping = model.mappingId;
    const NSUInteger rows[][2] = {{0,10},{10,66},{66,81},{81,100},{100,152},{152,172}};
    for (NSUInteger i = 0; i < 6; i++) {
      MarkdownASTNode *projection = [model projectBlockRange:NSMakeRange(rows[i][0], rows[i][1]-rows[i][0])];
      check(projection.children.count == 1, "independent full original row projection");
    }
    NSMutableAttributedString *first = renderNode([model projectBlockRange:NSMakeRange(10,56)]);
    check([first.string isEqualToString:@"first same link & 😀"], "original decoded rendering, no row reparse");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(6,4) inText:first], 18,22,"first same original span");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(11,4) inText:first],26,30,"link label span");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(16,1) inText:first],54,59,"entity spelling span");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(18,2) inText:first],60,62,"astral scalar span");
    NSArray<NSValue *> *highlights = [model renderedRangesForSourceRange:NSMakeRange(18,4) inText:first];
    check(highlights.count == 1 && NSEqualRanges(highlights.firstObject.rangeValue, NSMakeRange(6,4)),
          "saved source range recovers exact rendered characters");
    check([model projectBlockRange:NSMakeRange(18,4)] == nil, "partial paragraph projection rejected");
    check([model projectBlockRange:NSMakeRange(0,173)] == nil, "range beyond original rejected");
    check([model projectBlockRange:NSMakeRange(61,1)] == nil, "split original surrogate rejected");
    MarkdownASTNode *mutableProjection = [model projectBlockRange:NSMakeRange(10,56)];
    [mutableProjection.children removeAllObjects];
    check([model projectBlockRange:NSMakeRange(10,56)].children.count == 1 && [mapping isEqualToString:model.mappingId],
          "projection mutation cannot change original tree or mapping identity");
    ENRMOriginalDocumentModel *replacement = [[ENRMOriginalDocumentModel alloc] initWithSource:rich snapshotId:@"rich"];
    check(![replacement.mappingId isEqualToString:model.mappingId], "replacement model has a fresh mapping identity");
    check([replacement sourceRangeForRenderedRange:NSMakeRange(6,4) inText:first].location == NSNotFound,
          "old model fragments cannot anchor replacement snapshot");
    ENRMSourceFragment *foreign = [[ENRMSourceFragment alloc] initWithText:@"same" sourceRange:NSMakeRange(18,4) generated:NO];
    check([model sourceRangeForRenderedRange:NSMakeRange(0,4) inText:renderFragments(@[foreign])].location == NSNotFound,
          "invented attribute span cannot impersonate parser provenance");
    NSAttributedString *chrome = [[NSAttributedString alloc] initWithString:@"Copy"
                           attributes:@{ ENRMSourceFragmentAttributeName: NSNull.null }];
    check([model sourceRangeForRenderedRange:NSMakeRange(0,4) inText:chrome].location == NSNotFound,
          "renderer generated chrome remains unmapped without throwing");
    ENRMOriginalDocumentModel *breaks = [[ENRMOriginalDocumentModel alloc] initWithSource:@"one\r\ntwo\r\n" snapshotId:@"breaks"];
    MarkdownASTNode *breakNode = [breaks projectBlockRange:NSMakeRange(0,10)].children.firstObject.children[1];
    NSMutableAttributedString *space = [[NSMutableAttributedString alloc] initWithString:@" "];
    [breakNode applySourceFragmentsToString:space range:NSMakeRange(0,1)];
    checkRange([breaks sourceRangeForRenderedRange:NSMakeRange(0,1) inText:space],3,5,
               "actual soft-break renderer transformation retains original CRLF");
    NSMutableAttributedString *second = renderNode([model projectBlockRange:NSMakeRange(66,15)]);
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(7,4) inText:second],73,77,"second occurrence not searched");
    NSMutableAttributedString *table = renderNode([model projectBlockRange:NSMakeRange(100,52)]);
    // The flat parser fragments include header CellOther followed by same and tail.
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(9,4) inText:table],135,139,"table cell original span");
    NSAttributedString *body = renderFragments([model codeFragmentsForText:@"same()" blockRange:NSMakeRange(152,20)]);
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(0,4) inText:body],159,163,"rich Reader code original span");
    code(@"```text\n\n```\n",@"",13,8,"empty code projection");
    code(@"```text\nsame()\n```\n",@"same()",19,14,"zero trailing LF");
    code(@"```text\nsame()\n\n```\n",@"same()\n",20,15,"one trailing LF");
    code(@"```text\nsame()\n\n\n```\n",@"same()\n\n",21,16,"multiple trailing LF");
    code(@"```text\n\nsame()\n\nnext()\n\n```\n",@"\nsame()\n\nnext()\n",29,24,"leading and internal blanks");
    code(@"```text\n\tfirst\tsecond\n\n```\n",@"\tfirst\tsecond\n",27,22,"literal tab projection");
    code(@"```text\n😀 e\u0301 שלום same()\n\n```\n",@"😀 e\u0301 שלום same()\n",31,26,"astral combining and RTL projection");
    code(@"```text\n0123456789012345678901234567890123456789012345678901234567890123456789\n\n```\n",
         @"0123456789012345678901234567890123456789012345678901234567890123456789\n",84,79,"horizontal overflow literal projection");
    code(@"```text\nsame()\r\n\r\n\n```\n",@"same()\r\n\r\n",23,18,"literal CRLF preserved independently of normalized parser");
    code(@"```text\nsame()\r\n\r\n\n```\n",@"same()\n\n",23,18,"Reader normalized CRLF still has original span");
    NSString *reference = @"[same][ref]\r\n\r\n```mermaid\r\nx\r\n```\r\n\r\n[ref]: https://example.test\r\n";
    ENRMOriginalDocumentModel *refs = [[ENRMOriginalDocumentModel alloc] initWithSource:reference snapshotId:@"refs"];
    MarkdownASTNode *refrow = [refs projectBlockRange:NSMakeRange(0,15)];
    check(refrow.children.firstObject.children.firstObject.type == MarkdownNodeTypeLink,
          "original reference resolved across excluded Mermaid row");
    check([renderNode(refrow).string isEqualToString:@"same"],"reference label rendered from original nodes");
    ENRMOriginalDocumentModel *bom = [[ENRMOriginalDocumentModel alloc] initWithSource:@"\uFEFFsame\r\n" snapshotId:@"bom"];
    NSMutableAttributedString *bomText = renderNode([bom projectBlockRange:NSMakeRange(0,7)]);
    checkRange([bom sourceRangeForRenderedRange:NSMakeRange(0,4) inText:bomText],1,5,"original BOM keeps UTF16 coordinates");

    NSString *logical = @"Note\n\nfirst same link & 😀\n\nsecond same\n\nitem same\n\nCell\tOther\nsame\ttail\n\nsame()";
    check([model.selectionText.string isEqualToString:logical], "persistent original logical rendered document");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(12,4) inText:model.selectionText],18,22,
               "logical first rendered word uses original endpoints");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(12,27) inText:model.selectionText],18,77,
               "logical selection crosses independent original rows");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(63,4) inText:model.selectionText],135,139,
               "logical table cell uses original endpoints");
    checkRange([model sourceRangeForRenderedRange:NSMakeRange(74,4) inText:model.selectionText],159,163,
               "logical code uses original endpoints");
    check([model sourceRangeForRenderedRange:NSMakeRange(4,2) inText:model.selectionText].location == NSNotFound,
          "logical generated row separator remains unmapped");
    NSArray<NSValue *> *logicalHighlights = [model renderedRangesForSourceRange:NSMakeRange(18,4) inText:model.selectionText];
    check(logicalHighlights.count == 1 && NSEqualRanges(logicalHighlights.firstObject.rangeValue,NSMakeRange(12,4)),
          "original saved range maps back to logical rendered positions");
    check([refs.selectionText.string isEqualToString:@"same"],"logical diagram shapes remain excluded");
    ENRMOriginalDocumentModel *literal = [[ENRMOriginalDocumentModel alloc] initWithSource:@"```text\nsame()\r\n\r\n\n```\n"
                                                                            snapshotId:@"literal"];
    NSString *literalMapping = literal.mappingId;
    NSAttributedString *previous = literal.selectionText;
    check([literal adoptCodeText:@"same()\r\n\r\n" blockRange:NSMakeRange(0,23)], "verified mounted code literal adopted");
    check([literal.selectionText.string isEqualToString:@"same()\r\n\r\n"] &&
          [previous.string isEqualToString:@"same()\n\n"] && [literal.mappingId isEqualToString:literalMapping],
          "code presentation preserves exact literal, old logical snapshot and mapping identity");
    checkRange([literal sourceRangeForRenderedRange:NSMakeRange(0,10) inText:literal.selectionText],8,18,
               "adopted CRLF literal retains independently supplied original span");
    check(![literal adoptCodeText:@"same() WRONG" blockRange:NSMakeRange(0,23)] &&
          [literal.selectionText.string isEqualToString:@"same()\r\n\r\n"],"invalid code presentation cannot change model");
    ENRMOriginalDocumentModel *nested = [[ENRMOriginalDocumentModel alloc] initWithSource:@"> ```text\n> same()\n> ```\n"
                                                                           snapshotId:@"nested"];
    check([nested.selectionText.string isEqualToString:@"same()"],"nested original code remains in logical document");
  }
  return 0;
}
