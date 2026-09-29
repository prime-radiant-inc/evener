// New session's model picker (spec 11): the chosen host's models through the
// model sheet's own list (phase 3's ModelPicker), with "Hub default" first,
// up to five recent models, then each provider's, and what each model can do.
// It has no effort control: effort lives in one place, the form.
import type { ModelDescriptor } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { View } from "react-native";
import { useStore } from "zustand";
import { ChoiceRow, ModelPicker as ModelList } from "../ModelPicker";
import { creationModel } from "../newSession";
import { SearchField } from "../sheet/SearchField";
import { SheetStatus } from "../sheet/SheetStatus";
import { useColors } from "../ui";
import { type NewSessionRoutes, useNewSession } from "./newSessionContext";

/** Spec 11: "recent (up to five)". */
const RECENT_LIMIT = 5;

export function ModelPicker({ navigation }: NativeStackScreenProps<NewSessionRoutes, "Model">) {
	const { store } = useNewSession();
	const { models, recentModels, model, launchOverrides, loadingModels, submitting, selectModel } = useStore(store);
	const { palette } = useColors();
	const [query, setQuery] = useState("");
	const chosen = creationModel(models, model, launchOverrides);
	const choose = (next: ModelDescriptor | null) => {
		selectModel(next);
		navigation.goBack();
	};
	return (
		<View style={{ flex: 1, backgroundColor: palette.canvas }}>
			<SheetStatus />
			<View style={{ paddingHorizontal: 16, paddingVertical: 8 }}>
				<SearchField label="Search models" value={query} onChangeText={setQuery} />
			</View>
			<ModelList
				catalog={loadingModels ? null : { data: models, recent: recentModels.slice(0, RECENT_LIMIT) }}
				loading={loadingModels}
				query={query}
				visionOnly={false}
				current={chosen ? `${chosen.provider}/${chosen.model}` : ""}
				disabled={submitting}
				choose={choose}
				capabilities
				header={<ChoiceRow title="Hub default" selected={!chosen} disabled={submitting} onPress={() => choose(null)} />}
			/>
		</View>
	);
}
