// A question the agent asked earlier (spec 8.2, "Question (history)"): an
// amber left rule, each question in the reading serif, and your answer
// beneath. While a question is still open the dock is the question, so the
// transcript never shows the live one this way.
import type { AskUserQuestion } from "@evener/appwire-client";
import { Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useReadingFace } from "../display/displayContext";

export function QuestionHistory({
	questions,
	answer,
}: {
	questions: readonly AskUserQuestion[];
	answer: string | undefined;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	return (
		<View style={{ borderLeftWidth: 2, borderLeftColor: palette.attention, paddingLeft: 12, gap: 6 }}>
			{questions.map((question) => (
				<Text
					key={`${question.header}\u0000${question.question}`}
					allowFontScaling={allowFontScaling}
					style={{ ...face("regular"), fontSize: 15 * scale, lineHeight: 21 * scale, color: palette.prose }}
				>
					{question.question}
				</Text>
			))}
			{answer ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid }}
				>
					{`You answered: ${answer}`}
				</Text>
			) : null}
		</View>
	);
}
