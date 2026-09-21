import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type DefaultProfiles = components["schemas"]["DefaultProfilesResponse"];
export type RulesFile = components["schemas"]["RulesFileResponse"];
export type RoleProfile = components["schemas"]["RoleProfile"];

export const defaultProfilesQueryKey = ["default-profiles"] as const;
export const rulesFileQueryKey = (name: string) => ["rules-file", name] as const;

/**
 * The user-level default profiles and the daemon-wide rules files. Every
 * project's pickers union these with the project's own profiles, so a
 * failure to load must read as "no defaults", never as a broken form.
 */
export function useDefaultProfilesQuery(enabled = true) {
	return useQuery({
		queryKey: defaultProfilesQueryKey,
		queryFn: async (): Promise<DefaultProfiles> => {
			const { data, error } = await apiClient.GET("/api/v1/settings/profiles");
			if (error) throw new Error(apiErrorMessage(error));
			return data as DefaultProfiles;
		},
		retry: 1,
		enabled,
	});
}

export function useUpdateDefaultProfiles() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (profiles: Record<string, RoleProfile>) => {
			const { data, error } = await apiClient.PUT("/api/v1/settings/profiles", { body: { profiles } });
			if (error) throw new Error(apiErrorMessage(error));
			return data as DefaultProfiles;
		},
		onSuccess: (next) => queryClient.setQueryData<DefaultProfiles>(defaultProfilesQueryKey, next),
	});
}

/** One rules file's content; a file that does not exist yet resolves to null. */
export function useRulesFileQuery(name: string, enabled: boolean) {
	return useQuery({
		queryKey: rulesFileQueryKey(name),
		queryFn: async (): Promise<RulesFile | null> => {
			const { data, error, response } = await apiClient.GET("/api/v1/settings/rules/{name}", { params: { path: { name } } });
			if (response.status === 404) return null;
			if (error) throw new Error(apiErrorMessage(error));
			return data as RulesFile;
		},
		retry: false,
		enabled: enabled && name !== "",
	});
}

export function useUpdateRulesFile() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async ({ name, content }: { name: string; content: string }) => {
			const { data, error } = await apiClient.PUT("/api/v1/settings/rules/{name}", { params: { path: { name } }, body: { content } });
			if (error) throw new Error(apiErrorMessage(error));
			return data as RulesFile;
		},
		onSuccess: (file) => {
			queryClient.setQueryData<RulesFile | null>(rulesFileQueryKey(file.name), file);
			void queryClient.invalidateQueries({ queryKey: defaultProfilesQueryKey });
		},
	});
}

export function useDeleteRulesFile() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (name: string) => {
			const { error } = await apiClient.DELETE("/api/v1/settings/rules/{name}", { params: { path: { name } } });
			if (error) throw new Error(apiErrorMessage(error));
			return name;
		},
		onSuccess: (name) => {
			queryClient.setQueryData<RulesFile | null>(rulesFileQueryKey(name), null);
			void queryClient.invalidateQueries({ queryKey: defaultProfilesQueryKey });
		},
	});
}
